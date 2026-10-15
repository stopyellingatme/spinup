package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/build"
	"github.com/stopyellingatme/spinup/internal/db/lint"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const (
	testSHA     = "0123456789abcdef0123456789abcdef01234567"
	testRelease = "20261015T080000Z-0123456789ab"
	testRoot    = "/opt/todo"
	testRelDir  = "/opt/todo/releases/" + testRelease
	digestAPI   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestWeb   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

var testNow = time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

// testSecrets holds values that must never reach argv, output, or the journal.
var testSecrets = map[string]string{
	"API_TOKEN":         "tok-'quoted'-$(value)",
	"DATABASE_URL":      "postgres://app:hunter2-db-pass@postgres:5432/app?sslmode=disable",
	"POSTGRES_PASSWORD": "hunter2-db-pass",
}

// savingExec writes a small archive for docker save, so that the transport can stream it.
type savingExec struct{ *runexec.Fake }

func (s savingExec) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	if c.Name == "docker" && len(c.Args) > 2 && c.Args[0] == "save" {
		if err := os.WriteFile(c.Args[2], []byte("image-archive"), 0600); err != nil {
			return runexec.Result{}, err
		}
	}
	return s.Fake.Run(ctx, c)
}

type httpCall struct {
	status int
	body   string
	err    error
}

type fx struct {
	t         *testing.T
	root      string
	web       bool
	transport string
	exec      *runexec.Fake
	vm        *remote.Fake
	out       bytes.Buffer
	errOut    bytes.Buffer
	o         Options
	d         Deps
	built     []build.ImageOptions
	dialed    int
	hookOpts  []HookOptions
	hookCtx   []HookContext
	httpMu    sync.Mutex
	http      map[string][]httpCall
	httpLog   []string
	sleeps    []time.Duration
	findings  []lint.Finding
	checkErr  error
	decrypted map[string]string
}

func newFx(t *testing.T, web bool, transport string) *fx {
	t.Helper()
	x := &fx{t: t, root: t.TempDir(), web: web, transport: transport, exec: &runexec.Fake{}, vm: &remote.Fake{}, http: map[string][]httpCall{}, decrypted: testSecrets}
	toml := "behavior_version = 3\n\n[app]\nname = \"todo\"\nmodule = \"github.com/acme/todo\"\n\n[env.staging]\nhost = \"vm.example.com\"\nuser = \"deploy\"\npublic_url = \"https://todo.example.com\"\n"
	if transport != "" {
		toml += "transport = \"" + transport + "\"\n"
	}
	files := map[string]string{
		"spinup.toml":                         toml,
		"api/go.mod":                          "module github.com/acme/todo/api\n",
		"api/db/migrations/00001_init.sql":    "-- +goose Up\nCREATE TABLE a (id int);\n",
		"api/db/migrations/00002_more.sql":    "-- +goose Up\nCREATE TABLE b (id int);\n",
		"deploy/compose.yml":                  "name: todo-${SPINUP_COLOR}\n",
		"deploy/compose.override.yml":         "services: {}\n",
		"deploy/compose.edge.yml":             "name: todo\n",
		"deploy/compose.dev.yml":              "services: {}\n",
		"deploy/Caddyfile":                    "{$PUBLIC_HOST} {\n\timport upstream.caddy api 8080\n}\n",
		"deploy/Caddyfile.d/.gitkeep":         "",
		"deploy/Dockerfile.api":               "FROM scratch\n",
		"deploy/Dockerfile.postgres":          "FROM postgres\n",
		"deploy/postgres/postgresql.conf":     "max_connections = 200\n",
		"deploy/postgres/conf.d/.gitkeep":     "",
		"deploy/environments/staging.toml":    "smoke_urls = [\"/\"]\n[replicas]\napi = 2\n[resources.api]\ncpus = \"0.5\"\nmemory = \"256m\"\n",
		"deploy/environments/production.toml": "smoke_urls = []\n",
		"deploy/cloud-init/user-data.yaml":    "#cloud-config\n",
		"secrets/staging.enc.yaml":            "encrypted\n",
	}
	if web {
		files["deploy/Dockerfile.web"] = "FROM node\n"
	}
	for name, data := range files {
		p := filepath.Join(x.root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	x.o = Options{Dir: x.root, Env: "staging", Out: &x.out, ErrOut: &x.errOut, HealthTimeout: 4 * time.Second}
	x.d = Deps{
		Exec: savingExec{x.exec},
		Dial: func(context.Context, project.Env, bool) (remote.Runner, error) {
			x.dialed++
			return x.vm, nil
		},
		Decrypt: func(_ context.Context, root, env string) (map[string]string, error) {
			if root != x.root || env != "staging" {
				t.Errorf("decrypt %s %s", root, env)
			}
			return x.decrypted, nil
		},
		Check: func(context.Context, string, io.Writer) error { return x.checkErr },
		Lint:  func(string) ([]lint.Finding, error) { return x.findings, nil },
		Build: func(_ context.Context, _ runexec.Runner, o build.ImageOptions) ([]build.Image, error) {
			x.built = append(x.built, o)
			var out []build.Image
			for _, target := range o.Targets {
				img := build.Image{Name: "todo-" + target, Ref: "todo-" + target + ":" + o.Tag}
				if o.Push {
					img.Ref = "ghcr.io/acme/" + img.Ref
					img.Digest = map[string]string{"api": digestAPI, "web": digestWeb}[target]
				}
				out = append(out, img)
			}
			return out, nil
		},
		HTTPGet: func(_ context.Context, url string) (int, []byte, error) {
			x.httpMu.Lock()
			defer x.httpMu.Unlock()
			x.httpLog = append(x.httpLog, url)
			calls := x.http[url]
			if len(calls) == 0 {
				return 0, nil, errors.New("unexpected GET " + url)
			}
			c := calls[0]
			if len(calls) > 1 {
				x.http[url] = calls[1:]
			}
			return c.status, []byte(c.body), c.err
		},
		Now:   func() time.Time { return testNow },
		Sleep: func(_ context.Context, d time.Duration) error { x.sleeps = append(x.sleeps, d); return nil },
		User:  func() string { return "alice" },
		PreDeploy: func() []Hook {
			return []Hook{{Name: "backup", Run: func(ctx context.Context, c *HookContext) error {
				x.hookOpts = append(x.hookOpts, HookOptionsFrom(ctx))
				x.hookCtx = append(x.hookCtx, *c)
				return nil
			}}}
		},
		PostRelease: func() []Hook { return nil },
	}
	return x
}

// ok scripts a successful remote command.
func (x *fx) ok(argv ...string) { x.vm.Expect(argv...).Return("", "", 0) }

// journal scripts n journal appends.
func (x *fx) journal(n int) {
	for range n {
		x.vm.Expect("sh", "-c").Return("", "", 0)
	}
}

func (x *fx) expectLocalGit() {
	x.exec.Expect("git", "status", "--porcelain").Return("", "", 0)
	x.exec.Expect("git", "rev-parse", "HEAD").Return(testSHA+"\n", "", 0)
}

func (x *fx) expectConnect() {
	x.vm.Expect("sh", "-c", probeScript, "spinup-probe", testRoot).Return("1000\n1001\n", "", 0)
}

func (x *fx) expectLock() {
	x.vm.Expect("mkdir", testRoot+"/deploy.lock").Return("", "", 0)
	x.vm.ExpectUpload(testRoot+"/deploy.lock/owner.json").Return("", "", 0)
}

// expectBegin scripts the journal start of a new release: read, rotate, preflight, build.
func (x *fx) expectBegin(previous string) {
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases.json").Return("", "", 0)
	x.vm.Expect("sh", "-c", releaseExistsScript, "spinup-release-exists", testRelDir).Return("", "", 0)
	x.vm.Expect("sh", "-c").Return(previous, "", 0)
	x.ok("sh", "-c", rotateJournalScript, "spinup-journal", testRoot+"/deploy.state")
	x.journal(2)
}

func (x *fx) expectReadCurrent(current string) {
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/current/release.json").Return(current, "", 0)
}

func (x *fx) expectUpstream(content string) {
	x.vm.Expect("sh", "-c", readUpstreamScript, "spinup-upstream", testRoot+"/caddy/upstream.caddy").Return(content, "", 0)
}

// expectUploadRelease scripts the release directory, the secrets push, and release.json.
func (x *fx) expectUploadRelease(live string) {
	x.expectUpstream(live)
	x.vm.Expect("mkdir", "-p", "--", testRelDir).Return("", "", 0)
	for _, p := range x.releasePaths() {
		x.vm.ExpectUpload(testRelDir+"/"+p).Return("", "", 0)
	}
	x.vm.Expect("sh", "-c").Return("", "", 0) // secrets: prepare the directory
	x.vm.Expect("find", testRoot+"/secrets").Return(testRoot+"/secrets/OLD_NAME\n", "", 0)
	for _, name := range []string{"API_TOKEN", "DATABASE_URL", "POSTGRES_PASSWORD"} {
		x.vm.ExpectUpload(testRoot+"/secrets/"+name).Return("", "", 0)
	}
	x.ok("rm", "-f", "--", testRoot+"/secrets/OLD_NAME")
	x.vm.ExpectUpload(testRelDir+"/release.json").Return("", "", 0)
}

func (x *fx) releasePaths() []string {
	return []string{".env", "Caddyfile", "Dockerfile.postgres", "compose.edge.yml", "compose.override.yml", "compose.yml", "postgres/postgresql.conf"}
}

// expectEdge scripts the configuration sync and the base project start.
func (x *fx) expectEdge(live, caddy string) {
	x.ok("sh", "-c", edgeSyncScript, "spinup-edge", testRoot, testRelDir)
	x.expectUpstream(live)
	if live == "" {
		x.vm.ExpectUpload(testRoot+"/caddy/upstream.caddy").Return("", "", 0)
	}
	x.vm.Expect(psArgv("todo", "caddy")...).Return(caddy, "", 0)
	if caddy != "" {
		x.ok("docker", "exec", strings.TrimSpace(caddy), "caddy", "validate", "--config", "/etc/caddy/.next/Caddyfile", "--adapter", "caddyfile")
	} else {
		x.ok(ComposeArgv("todo", testRelDir, []string{testRelDir + "/compose.edge.yml"}, "run", "--rm", "--no-deps", "-T", "--entrypoint", "caddy", "caddy", "validate", "--config", "/etc/caddy/.next/Caddyfile", "--adapter", "caddyfile")...)
	}
	x.ok("sh", "-c", edgeInstallScript, "spinup-edge-install", testRoot)
	x.ok(ComposeArgv("todo", testRelDir, []string{testRelDir + "/compose.edge.yml"}, "up", "-d", "--wait", "--wait-timeout", "4")...)
}

func (x *fx) expectMigrate() {
	x.ok(ComposeArgv("todo-migrate", testRelDir, []string{testRelDir + "/compose.yml"}, "--profile", "migrate", "run", "--rm", "-T", "migrate")...)
}

func (x *fx) colorArgv(color string, args ...string) []string {
	return ComposeArgv("todo-"+color, testRelDir, []string{testRelDir + "/compose.yml"}, args...)
}

func psArgv(project, service string) []string {
	argv := []string{"docker", "ps", "-q", "--filter", "status=running", "--filter", "label=com.docker.compose.project=" + project}
	if service != "" {
		argv = append(argv, "--filter", "label=com.docker.compose.service="+service)
	}
	return argv
}

func inspectArgv(ids ...string) []string {
	return append([]string{"docker", "inspect", "--format", `{{with index .NetworkSettings.Networks "todo-app"}}{{.IPAddress}}{{end}}`}, ids...)
}

func wgetReady(addr string, port int, path string) []string {
	return []string{"docker", "exec", "caddy1", "wget", "-q", "-T", "3", "-O", "/dev/null", "http://" + hostPort(addr, port) + path}
}

// expectReady scripts one successful readiness pass of the new color.
func (x *fx) expectReady(color string) {
	x.vm.Expect(psArgv("todo-"+color, "api")...).Return("napi1\nnapi2\n", "", 0)
	x.vm.Expect(inspectArgv("napi1", "napi2")...).Return("10.0.1.1\n10.0.1.2\n", "", 0)
	x.ok(wgetReady("10.0.1.1", 8080, "/readyz")...)
	x.ok(wgetReady("10.0.1.2", 8080, "/readyz")...)
	if x.web {
		x.vm.Expect(psArgv("todo-"+color, "web")...).Return("nweb1\n", "", 0)
		x.vm.Expect(inspectArgv("nweb1")...).Return("10.0.1.3\n", "", 0)
		x.ok(wgetReady("10.0.1.3", 3000, "/healthz")...)
	}
}

func (x *fx) reloadArgv() []string {
	return []string{"docker", "exec", "caddy1", "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}
}

func (x *fx) drainArgv() []string {
	return []string{"docker", "exec", "caddy1", "wget", "-q", "-T", "2", "-O", "-", "http://127.0.0.1:2019/reverse_proxy/upstreams"}
}

// expectSwitch scripts a successful switch from live (or no live color) to color.
func (x *fx) expectSwitch(upstream, color string, oldAddrs bool) {
	old := OtherColor(color)
	x.expectUpstream(upstream)
	x.vm.Expect(psArgv("todo", "caddy")...).Return("caddy1\n", "", 0)
	x.ok(x.colorArgv(color, "up", "-d", "--wait", "--wait-timeout", "4", "--remove-orphans")...)
	x.expectReady(color)
	if oldAddrs {
		x.vm.Expect(psArgv("todo-"+old, "")...).Return("oapi1\n", "", 0)
		x.vm.Expect(inspectArgv("oapi1")...).Return("10.0.0.9\n", "", 0)
	} else {
		x.vm.Expect(psArgv("todo-"+old, "")...).Return("", "", 0)
	}
	x.vm.ExpectUpload(testRelDir+"/upstream.caddy").Return("", "", 0)
	x.vm.ExpectUpload(testRoot+"/caddy/upstream.caddy").Return("", "", 0)
	x.ok(x.reloadArgv()...)
	if oldAddrs {
		x.vm.Expect(x.drainArgv()...).Return(`[{"address":"10.0.0.9:8080","num_requests":2,"fails":0}]`, "", 0)
		x.vm.Expect(x.drainArgv()...).Return(`[{"address":"10.0.0.9:8080","num_requests":0,"fails":0},{"address":"10.0.1.1:8080","num_requests":3,"fails":0}]`, "", 0)
	}
	x.ok(x.colorArgv(old, "down", "-t", "15", "--remove-orphans")...)
}

func (x *fx) expectSmoke(color string) {
	x.http["https://todo.example.com/healthz"] = []httpCall{{status: 200}}
	x.http["https://todo.example.com/"] = []httpCall{{status: 502}, {status: 200}}
	version := `{"name":"todo","version":"` + testRelease + `"}`
	if x.web {
		x.vm.Expect(psArgv("todo", "caddy")...).Return("caddy1\n", "", 0)
		x.vm.Expect("docker", "exec", "caddy1", "wget", "-q", "-T", "5", "-O", "-", "http://api-"+color+":8080/version").Return(version, "", 0)
	} else {
		x.http["https://todo.example.com/version"] = []httpCall{{status: 200, body: version}}
	}
}

// expectRecord scripts the current link, releases.json, and the release directories.
func (x *fx) expectRecord(history, dirs string) {
	x.ok("ln", "-sfn", "releases/"+testRelease, testRoot+"/current")
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases.json").Return(history, "", 0)
	x.vm.ExpectUpload(testRoot+"/releases.json").Return("", "", 0)
	x.vm.Expect("sh", "-c", listReleasesScript, "spinup-releases", testRoot+"/releases").Return(dirs, "", 0)
}

func (x *fx) expectUnlockAndClose() {
	x.vm.Expect("sh", "-c").Return("", "", 0)
	x.vm.ExpectClose().Return("", "", 0)
}

func (x *fx) run() error {
	x.t.Helper()
	return Deploy(context.Background(), x.o, x.d)
}

// journalEvents returns "step:status" for each journal append, in order.
func (x *fx) journalEvents() []string {
	var out []string
	for _, e := range x.journalEntries() {
		out = append(out, e.Step+":"+e.Status)
	}
	return out
}

func (x *fx) journalEntries() []Entry {
	var out []Entry
	for _, c := range x.vm.Calls() {
		if len(c.Argv) == 3 && c.Argv[0] == "sh" && strings.HasSuffix(c.Argv[2], ">> '"+testRoot+"/deploy.state'") {
			raw := strings.TrimPrefix(c.Argv[2], "printf '%s\\n' ")
			raw = strings.TrimSuffix(raw, " >> '"+testRoot+"/deploy.state'")
			raw = strings.ReplaceAll(strings.Trim(raw, "'"), `'"'"'`, "'")
			var e Entry
			if err := json.Unmarshal([]byte(raw), &e); err != nil {
				x.t.Fatalf("journal line %q: %v", raw, err)
			}
			out = append(out, e)
		}
	}
	return out
}

func (x *fx) upload(path string) []byte {
	x.t.Helper()
	for _, u := range x.vm.Uploads() {
		if u.Path == path {
			return u.Data
		}
	}
	x.t.Fatalf("no upload of %s", path)
	return nil
}

// assertNoSecrets checks that no secret value reached argv, output, the journal, or a non-secret upload.
func (x *fx) assertNoSecrets() {
	x.t.Helper()
	var hay []string
	for _, c := range x.vm.Calls() {
		hay = append(hay, strings.Join(c.Argv, "\x00"))
	}
	for _, c := range x.exec.Calls() {
		hay = append(hay, c.Name+"\x00"+strings.Join(c.Args, "\x00")+strings.Join(c.Env, "\x00"))
	}
	for _, u := range x.vm.Uploads() {
		if !strings.HasPrefix(u.Path, testRoot+"/secrets/") {
			hay = append(hay, u.Path+"\x00"+string(u.Data))
		}
	}
	hay = append(hay, x.out.String(), x.errOut.String())
	for _, h := range hay {
		for name, value := range testSecrets {
			if strings.Contains(h, value) {
				x.t.Fatalf("secret %s leaked into %q", name, h)
			}
		}
	}
}
