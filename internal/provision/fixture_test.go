package provision

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean/dofake"
	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/secrets"
	"github.com/stopyellingatme/spinup/internal/ui"
)

const sampleConfig = `# Project settings. Keep this comment.
[app]
name = "demo"
module = "example.com/demo"

# The staging environment stays as it is.
[env.staging]
host = "198.51.100.5"
user = "deploy"
`

const sampleUserData = `#cloud-config
users:
  - name: deploy
    ssh_authorized_keys:
      - SPINUP_DEPLOY_SSH_KEY
packages:
  - docker.io
`

const sampleEnvFile = `smoke_urls = ["/healthz"]

[resources.postgres]
cpus = "2.0"
memory = "2g"
`

// sshPublicKey makes a valid ed25519 public key line.
func sshPublicKey(t testing.TB) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return wireKey(pub)
}

func wireKey(pub []byte) string {
	var b []byte
	for _, s := range [][]byte{[]byte("ssh-ed25519"), pub} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b)
}

// execFake is a local runner for git, docker, and ssh-keygen.
type execFake struct {
	mu        sync.Mutex
	calls     []runexec.Cmd
	porcelain string
	staged    string
	dockerErr error
	keygens   int
}

func (e *execFake) Run(_ context.Context, c runexec.Cmd) (runexec.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, c)
	switch c.Name {
	case "ssh-keygen":
		e.keygens++
		path := c.Args[len(c.Args)-1]
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		priv := "-----BEGIN OPENSSH PRIVATE KEY-----\nSECRET-HOST-KEY-" + string(rune('A'+e.keygens)) + "\n-----END OPENSSH PRIVATE KEY-----\n"
		if err := os.WriteFile(path, []byte(priv), 0o600); err != nil {
			return runexec.Result{}, err
		}
		if err := os.WriteFile(path+".pub", []byte(wireKey(pub)+" \n"), 0o644); err != nil {
			return runexec.Result{}, err
		}
	case "docker":
		return runexec.Result{}, e.dockerErr
	case "git":
		switch c.Args[0] {
		case "status":
			return runexec.Result{Stdout: []byte(e.porcelain)}, nil
		case "diff":
			return runexec.Result{Stdout: []byte(e.staged)}, nil
		}
	}
	return runexec.Result{}, nil
}

func (e *execFake) gitCalls(sub string) [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out [][]string
	for _, c := range e.calls {
		if c.Name == "git" && c.Args[0] == sub {
			out = append(out, c.Args)
		}
	}
	return out
}

// vmFake is the remote runner of the droplet.
type vmFake struct {
	mu      sync.Mutex
	handler func(argv []string) (stdout, stderr string, code int, err error)
	calls   [][]string
	closes  int
	dials   int
}

func (v *vmFake) Run(_ context.Context, c remote.Cmd) (runexec.Result, error) {
	v.mu.Lock()
	v.calls = append(v.calls, c.Argv)
	h := v.handler
	v.mu.Unlock()
	stdout, stderr, code, err := "", "", 0, error(nil)
	if h != nil {
		stdout, stderr, code, err = h(c.Argv)
	}
	if c.Stdout != nil {
		_, _ = c.Stdout.Write([]byte(stdout))
	}
	if c.Stderr != nil {
		_, _ = c.Stderr.Write([]byte(stderr))
	}
	res := runexec.Result{Stdout: []byte(stdout), Stderr: []byte(stderr), ExitCode: code}
	if err == nil && code != 0 {
		err = &runexec.ExitError{Cmd: strings.Join(c.Argv, " "), Code: code, StderrTail: stderr}
	}
	return res, err
}

func (v *vmFake) Upload(context.Context, []byte, string, fs.FileMode) error { return nil }

func (v *vmFake) Close(context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closes++
	return nil
}

func (v *vmFake) ran(first string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for _, c := range v.calls {
		if len(c) > 0 && c[0] == first {
			n++
		}
	}
	return n
}

// secretsFake keeps secrets in memory.
type secretsFake struct {
	mu      sync.Mutex
	root    string
	exists  bool
	vals    map[string]string
	inits   []secrets.Options
	sets    []string
	setFail error
}

func (s *secretsFake) Exists(string, string) bool { return s.exists }

func (s *secretsFake) Init(_ context.Context, o secrets.Options) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inits = append(s.inits, o)
	s.exists = true
	if err := os.MkdirAll(filepath.Join(s.root, "secrets"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.root, "secrets", o.Env+".enc.yaml"), []byte("sops: {}\n"), 0o644)
}

func (s *secretsFake) Decrypt(context.Context, string, string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.vals {
		out[k] = v
	}
	return out, nil
}

func (s *secretsFake) Set(_ context.Context, _ secrets.Options, name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setFail != nil {
		return s.setFail
	}
	if s.vals == nil {
		s.vals = map[string]string{}
	}
	s.vals[name] = value
	s.sets = append(s.sets, name)
	return nil
}

// clock is a fake clock. Sleep moves it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return nil
}

type fixture struct {
	t        *testing.T
	root     string
	srv      *dofake.Server
	cloud    *digitalocean.Client
	exec     *execFake
	vm       *vmFake
	sec      *secretsFake
	clock    *clock
	out, err *bytes.Buffer
	deploys  []deploy.Options
	deployFn func(deploy.Options) error
	resolve  func(host string) []netip.Addr
	dialErr  error
	deps     Deps
	opts     Options
	// atCreate records spinup.toml host_keys when the create request arrives.
	atCreate []string
	userData string
	fakeOpts dofake.Options
}

func newFixture(t *testing.T, mutate func(*fixture)) *fixture {
	t.Helper()
	t.Setenv("SPINUP_KEYS_DIR", t.TempDir())
	f := &fixture{t: t, root: t.TempDir(), exec: &execFake{}, vm: &vmFake{}, clock: &clock{t: time.Unix(1_700_000_000, 0)}, out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	f.sec = &secretsFake{root: f.root}
	write := func(name, data string) {
		p := filepath.Join(f.root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("spinup.toml", sampleConfig)
	write("deploy/cloud-init/user-data.yaml", sampleUserData)
	write("deploy/environments/production.toml", sampleEnvFile)
	write("deploy/environments/staging.toml", "smoke_urls = [\"/healthz\"]\n")
	write("operator.pub", sshPublicKey(t)+" me@laptop\n")
	f.vm.handler = func(argv []string) (string, string, int, error) {
		if argv[0] == "cloud-init" {
			return "status: done\n", "", 0, nil
		}
		return "", "", 0, nil
	}
	f.resolve = func(string) []netip.Addr { return nil }
	f.fakeOpts = dofake.Options{StaticTokens: []string{"tok"}, TeamUUID: "team-1", TeamName: "Team One"}
	if mutate != nil {
		mutate(f)
	}
	f.fakeOpts.OnCreateDroplet = func(req map[string]any) error {
		if cfg, err := project.Load(f.root); err == nil {
			f.atCreate = cfg.Envs["production"].HostKeys
		}
		f.userData, _ = req["user_data"].(string)
		return nil
	}
	f.srv = dofake.New(f.fakeOpts)
	url := f.srv.Start()
	t.Cleanup(f.srv.Close)
	f.cloud = digitalocean.New(url, func(context.Context) (string, error) { return "tok", nil })
	f.cloud.Sleep = func(context.Context, time.Duration) error { return nil }
	f.deps = Deps{
		Exec:  f.exec,
		Cloud: f.cloud,
		Dial: func(context.Context, project.Env) (remote.Runner, error) {
			f.vm.mu.Lock()
			f.vm.dials++
			f.vm.mu.Unlock()
			if f.dialErr != nil {
				return nil, f.dialErr
			}
			return f.vm, nil
		},
		Deploy: func(_ context.Context, o deploy.Options) error {
			f.deploys = append(f.deploys, o)
			if f.deployFn != nil {
				return f.deployFn(o)
			}
			return nil
		},
		Secrets: f.sec,
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			return f.resolve(host), nil
		},
		Now:      f.clock.Now,
		Sleep:    f.clock.Sleep,
		LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
	}
	f.opts = Options{
		Dir: f.root, SSHKey: filepath.Join(f.root, "operator.pub"), NoRecovery: true,
		Confirm: project.Confirm{Yes: true}, Out: f.out, ErrOut: f.err,
		Deploy: deploy.Options{},
	}
	return f
}

func (f *fixture) up() error { return Up(context.Background(), f.opts, f.deps) }

func (f *fixture) config() *project.Config {
	f.t.Helper()
	cfg, err := project.Load(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	return cfg
}

func (f *fixture) configText() string {
	b, err := os.ReadFile(filepath.Join(f.root, "spinup.toml"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// mutations counts the requests that change the cloud.
func (f *fixture) mutations() int {
	n := 0
	for _, r := range f.srv.Requests() {
		if r.Method != http.MethodGet && !strings.HasPrefix(r.Path, "/v1/") {
			n++
		}
	}
	return n
}

// uiErr returns the user-facing error in err.
func uiErr(t *testing.T, err error) *ui.Error {
	t.Helper()
	var ue *ui.Error
	if !errors.As(err, &ue) {
		t.Fatalf("want a *ui.Error, got %v", err)
	}
	if ue.Try == "" {
		t.Errorf("error has no Try line: %v", err)
	}
	return ue
}

func digitalocean2Req() digitalocean.DropletRequest {
	return digitalocean.DropletRequest{Name: "other", Region: "nyc3", Size: "s-1vcpu-2gb", Image: "ubuntu-24-04-x64", Tags: []string{"spinup", "spinup:demo:production"}}
}

func digitalocean2Rec(ip string) digitalocean.Record {
	return digitalocean.Record{Type: "A", Name: "app", Data: ip, TTL: 300}
}
