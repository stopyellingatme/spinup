package provision

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestUpHappyPath(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if got := f.srv.Droplets(); len(got) != 1 || got[0].Name != "demo-production" || got[0].Size != "s-2vcpu-4gb" || got[0].Region != "nyc3" {
		t.Fatalf("droplets = %+v", got)
	}
	if n := len(f.srv.Firewalls()); n != 1 {
		t.Fatalf("firewalls = %d", n)
	}
	if n := len(f.srv.SSHKeys()); n != 1 {
		t.Fatalf("ssh keys = %d", n)
	}
	cfg := f.config()
	env := cfg.Envs["production"]
	if env.Host != "203.0.113.10" || env.User != "deploy" || env.PublicURL != "https://203-0-113-10.sslip.io" || len(env.HostKeys) != 1 {
		t.Fatalf("env = %+v", env)
	}
	if cfg.DefaultEnv != "production" {
		t.Errorf("default_env = %q", cfg.DefaultEnv)
	}
	// The injected key is pinned before the create. The rotation then replaces it with the key
	// that the VM made, so only that key stays pinned.
	if len(f.atCreate) != 1 || f.atCreate[0] == env.HostKeys[0] {
		t.Errorf("host_keys at create = %v, final = %v; want one injected key, then a new key", f.atCreate, env.HostKeys)
	}
	if !f.vm.rotated || env.HostKeys[0] != f.vm.served {
		t.Errorf("rotation: rotated = %v, served = %q, pinned = %v", f.vm.rotated, f.vm.served, env.HostKeys)
	}
	text := f.configText()
	for _, want := range []string{"# Project settings. Keep this comment.", "# The staging environment stays as it is.", `host = "198.51.100.5"`} {
		if !strings.Contains(text, want) {
			t.Errorf("spinup.toml lacks %q:\n%s", want, text)
		}
	}
	st, err := readStored(f.root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if st.Team != "team-1" || st.FirewallID == "" || st.DropletID != int64(f.srv.Droplets()[0].ID) || st.Tag != "spinup:demo:production" || st.Region != "nyc3" || st.Image != "ubuntu-24-04-x64" || st.Domain != "" {
		t.Errorf("stored = %+v", st)
	}
	for _, want := range []string{"ssh_keys:", "ed25519_private: |", "SECRET-HOST-KEY", "ed25519_public: ssh-ed25519 ", "ssh-ed25519 "} {
		if !strings.Contains(f.userData, want) {
			t.Errorf("user data lacks %q:\n%s", want, f.userData)
		}
	}
	if !strings.HasPrefix(f.userData, "#cloud-config") {
		t.Errorf("user data does not start with #cloud-config:\n%s", f.userData)
	}
	operator := strings.Fields(readFile(t, f.root, "operator.pub"))[1]
	if !strings.Contains(f.userData, operator) {
		t.Error("user data lacks the operator key")
	}
	if all := f.out.String() + f.err.String(); strings.Contains(all, "SECRET-HOST-KEY") || strings.Contains(all, "BEGIN OPENSSH") {
		t.Errorf("output holds the private key:\n%s", all)
	}
	if got := f.sec.sets; len(got) != 2 {
		t.Errorf("secrets set = %v", got)
	}
	if len(f.sec.inits) != 1 || !f.sec.inits[0].NoRecovery {
		t.Errorf("inits = %+v", f.sec.inits)
	}
	if adds := f.exec.gitCalls("add"); len(adds) != 1 || strings.Join(adds[0], " ") != "add -- spinup.toml secrets/production.enc.yaml" {
		t.Errorf("git add = %v", adds)
	}
	if commits := f.exec.gitCalls("commit"); len(commits) != 0 {
		t.Errorf("committed without staged changes: %v", commits)
	}
	if len(f.deploys) != 1 || f.deploys[0].Env != "production" || f.deploys[0].Dir != f.root {
		t.Errorf("deploys = %+v", f.deploys)
	}
	if f.vm.ran("cloud-init") != 1 || f.vm.closes < 2 {
		t.Errorf("cloud-init runs %d, closes %d", f.vm.ran("cloud-init"), f.vm.closes)
	}
	if !strings.Contains(f.out.String(), "Region: nyc3 (change with --region)") {
		t.Errorf("no region hint:\n%s", f.out.String())
	}
}

func TestUpCommitsWhenStaged(t *testing.T) {
	f := newFixture(t, nil)
	f.exec.staged = "spinup.toml\n"
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	commits := f.exec.gitCalls("commit")
	if len(commits) != 1 || commits[0][2] != "spinup: provision production on DigitalOcean" {
		t.Fatalf("commits = %v", commits)
	}
}

func TestUpIsIdempotent(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	before := len(f.srv.Requests())
	toml := f.configText()
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.srv.Requests()[before:] {
		if r.Method != http.MethodGet {
			t.Errorf("second run sent %s %s", r.Method, r.Path)
		}
	}
	if f.configText() != toml {
		t.Errorf("second run changed spinup.toml:\n%s", f.configText())
	}
	if len(f.srv.Droplets()) != 1 || len(f.sec.sets) != 2 || f.exec.keygens != 1 {
		t.Errorf("droplets %d, sets %v, keygens %d", len(f.srv.Droplets()), f.sec.sets, f.exec.keygens)
	}
	if len(f.deploys) != 2 {
		t.Errorf("deploys = %d", len(f.deploys))
	}
}

func TestUpConvergesAfterFailures(t *testing.T) {
	cases := []struct {
		name, method, prefix string
		status               int
		billing              bool
	}{
		{"account", "GET", "/v2/account", 403, false},
		{"ssh key", "POST", "/v2/account/keys", 500, false},
		{"firewall", "POST", "/v2/firewalls", 500, false},
		{"droplet create", "POST", "/v2/droplets", 500, false},
		{"wait", "GET", "/v2/droplets/", 403, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.srv.FailNext(tc.method, tc.prefix, tc.status, 1)
			err := f.up()
			if err == nil {
				t.Fatal("want an error")
			}
			_ = uiErr(t, err)
			if got := strings.Contains(f.err.String(), "spinup deploy digital-ocean destroy --env production --i-know"); got != tc.billing {
				t.Errorf("billing note = %v, want %v:\n%s", got, tc.billing, f.err.String())
			}
			if err := f.up(); err != nil {
				t.Fatalf("second run: %v", err)
			}
			if len(f.srv.Droplets()) != 1 || len(f.srv.Firewalls()) != 1 || len(f.srv.SSHKeys()) != 1 {
				t.Errorf("droplets %d, firewalls %d, keys %d", len(f.srv.Droplets()), len(f.srv.Firewalls()), len(f.srv.SSHKeys()))
			}
			if env := f.config().Envs["production"]; env.Host == "" || len(env.HostKeys) != 1 {
				t.Errorf("env = %+v", env)
			}
		})
	}
}

func TestUpBillingNoteNamesTheDroplet(t *testing.T) {
	f := newFixture(t, nil)
	f.deployFn = func(deploy.Options) error { return errors.New("health check failed") }
	err := f.up()
	ue := uiErr(t, err)
	if !strings.Contains(ue.Try, "spinup deploy --env production --resume") {
		t.Errorf("Try = %q", ue.Try)
	}
	got := f.err.String()
	for _, want := range []string{"demo-production", "ID " + strconv.Itoa(f.srv.Droplets()[0].ID), "$0.03571/hour", "destroy --env production --i-know"} {
		if !strings.Contains(got, want) {
			t.Errorf("ErrOut lacks %q:\n%s", want, got)
		}
	}
}

func TestUpCloudInitExit(t *testing.T) {
	for _, tc := range []struct {
		code    int
		wantErr bool
	}{{1, true}, {2, false}} {
		f := newFixture(t, nil)
		f.vm.handler = func(argv []string) (string, string, int, error) {
			if argv[0] == "cloud-init" {
				return "status: error\nerrors: [bad module]\n", "", tc.code, nil
			}
			return "", "", 0, nil
		}
		err := f.up()
		if (err != nil) != tc.wantErr {
			t.Fatalf("code %d: err = %v", tc.code, err)
		}
		if tc.wantErr {
			ue := uiErr(t, err)
			if !strings.Contains(ue.Try, "--recreate") || !strings.Contains(ue.Why, "bad module") {
				t.Errorf("error = %+v", ue)
			}
			continue
		}
		if !strings.Contains(f.err.String(), "bad module") || len(f.deploys) != 1 {
			t.Errorf("exit 2: warn %q, deploys %d", f.err.String(), len(f.deploys))
		}
	}
}

func hostKeyErr() (string, string, int, error) {
	return "", "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@\n", 255, nil
}

func TestUpHostKeyErrorAfterWaitIsFatal(t *testing.T) {
	f := newFixture(t, nil)
	f.vm.handler = func([]string) (string, string, int, error) { return hostKeyErr() }
	err := f.up()
	ue := uiErr(t, err)
	if !strings.Contains(ue.Why, "does not match the pin") || !strings.Contains(ue.Try, "--recreate") {
		t.Errorf("error = %+v", ue)
	}
	if len(f.deploys) != 0 {
		t.Error("deployed after a host key error")
	}
}

func TestUpHostKeyErrorIsRetriedDuringWait(t *testing.T) {
	f := newFixture(t, nil)
	n := 0
	f.vm.handler = func(argv []string) (string, string, int, error) {
		n++
		if n <= 3 {
			return hostKeyErr()
		}
		return "", "", 0, nil
	}
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
}

func TestUpHostKeyErrorAfterConnectIsFatalAtOnce(t *testing.T) {
	f := newFixture(t, nil)
	f.vm.handler = func(argv []string) (string, string, int, error) {
		if argv[0] == "cloud-init" {
			return "", "Host key verification failed.\n", 255, nil
		}
		return "", "", 0, nil
	}
	err := f.up()
	ue := uiErr(t, err)
	if !strings.Contains(ue.What, "host key") && !strings.Contains(ue.Why, "pin") {
		t.Errorf("error = %+v", ue)
	}
}

func TestUpSSHWaitTimesOut(t *testing.T) {
	f := newFixture(t, nil)
	f.vm.handler = func([]string) (string, string, int, error) {
		return "", "ssh: connect to host: Connection refused\n", 255, nil
	}
	start := f.clock.Now()
	err := f.up()
	ue := uiErr(t, err)
	if !strings.Contains(ue.Why, "Connection refused") {
		t.Errorf("error = %+v", ue)
	}
	if got := f.clock.Now().Sub(start); got < sshWaitTotal {
		t.Errorf("the SSH wait ended after %v, want at least %v", got, sshWaitTotal)
	}
	if n := f.vm.ran("true"); n < 2 {
		t.Errorf("the SSH wait tried %d times, want several attempts", n)
	}
}

func TestUpPlanMakesNoWrites(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.Plan = true
	f.opts.Domain = "app.example.com"
	before := f.configText()
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if f.mutations() != 0 {
		t.Errorf("plan changed the cloud: %v", f.srv.Requests())
	}
	if f.configText() != before {
		t.Error("plan changed spinup.toml")
	}
	if len(f.exec.gitCalls("add")) != 0 || len(f.deploys) != 0 || len(f.sec.inits) != 0 || f.exec.keygens != 0 {
		t.Error("plan had side effects")
	}
	out := f.out.String()
	for _, want := range []string{"SSH key: create", "Firewall: create spinup-demo-production", "Droplet: create demo-production, size s-2vcpu-4gb", "$24.00/month", "DNS: DigitalOcean does not hold a zone", "Secrets: create secrets/production.enc.yaml", "Secrets: make DATABASE_URL, POSTGRES_PASSWORD", "No changes made"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
}

func TestUpNeedsYesWithoutTerminal(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.Confirm = project.Confirm{}
	err := f.up()
	ue := uiErr(t, err)
	if !strings.Contains(ue.Try, "--yes") {
		t.Errorf("Try = %q", ue.Try)
	}
	if f.mutations() != 0 {
		t.Error("created resources before the price answer")
	}
}

func TestUpPriceQuestion(t *testing.T) {
	for _, tc := range []struct {
		answer string
		ok     bool
	}{{"y\n", true}, {"yes\n", true}, {"n\n", false}, {"\n", false}} {
		f := newFixture(t, nil)
		prompt := &strings.Builder{}
		f.opts.Confirm = project.Confirm{Interactive: true, In: strings.NewReader(tc.answer), Out: prompt}
		err := f.up()
		if (err == nil) != tc.ok {
			t.Errorf("answer %q: err = %v", tc.answer, err)
		}
		if !strings.Contains(prompt.String(), "Create droplet s-2vcpu-4gb in nyc3 for $24.00/month ($0.03571/hour)?") {
			t.Errorf("prompt = %q", prompt.String())
		}
	}
}

func TestUpTeamMismatch(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	other := newFixture(t, func(o *fixture) { o.fakeOpts.TeamUUID = "team-2" })
	// Copy the provisioned config into the other project.
	writeFile(t, other.root, "spinup.toml", f.configText())
	err := other.up()
	ue := uiErr(t, err)
	if !strings.Contains(ue.Try, "spinup deploy digital-ocean login") || !strings.Contains(ue.Why, "team-1") {
		t.Errorf("error = %+v", ue)
	}
	if other.mutations() != 0 {
		t.Error("created resources for the wrong team")
	}
}

func TestUpSizeTooSmall(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.Size = "s-1vcpu-1gb"
	err := f.up()
	ue := uiErr(t, err)
	if !strings.Contains(ue.Why, "1 vCPUs") {
		t.Errorf("error = %+v", ue)
	}
	if f.mutations() != 0 {
		t.Error("created resources")
	}
	// A staging environment has no cpus limit, but the memory minimum still applies.
	g := newFixture(t, nil)
	g.opts.Env, g.opts.Size = "staging", "s-1vcpu-1gb"
	if err := g.up(); err == nil || !strings.Contains(uiErr(t, err).Why, "1024 MiB") {
		t.Errorf("staging error = %v", err)
	}
}

func TestUpSizeDefaultsByEnv(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.Env = "staging"
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if got := f.srv.Droplets()[0].Size; got != "s-1vcpu-2gb" {
		t.Errorf("staging size = %s", got)
	}
	if cfg := f.config(); cfg.DefaultEnv != "staging" {
		t.Errorf("default_env = %q", cfg.DefaultEnv)
	}
}

func TestUpRefusesNonUbuntu(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.Image = "debian-12-x64"
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.What, "debian-12-x64") {
		t.Errorf("error = %+v", ue)
	}
	if len(f.srv.Requests()) != 0 {
		t.Error("called the API before the preflight failed")
	}
}

func TestUpRefusesSkipBuildWhenURLChanges(t *testing.T) {
	f := newFixture(t, nil)
	writeFile(t, f.root, "deploy/Dockerfile.web", "FROM scratch\n")
	f.opts.Deploy.SkipBuild = true
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.What, "--skip-build") {
		t.Errorf("error = %+v", ue)
	}
	// An API-only app has no web image, so it can reuse its images with a new URL.
	g := newFixture(t, nil)
	g.opts.Deploy.SkipBuild = true
	if err := g.up(); err != nil {
		t.Errorf("API-only --skip-build: %v", err)
	}
}

func TestUpPreflightTools(t *testing.T) {
	f := newFixture(t, nil)
	f.deps.LookPath = func(n string) (string, error) {
		if n == "sops" {
			return "", errors.New("missing")
		}
		return n, nil
	}
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.What, "sops") {
		t.Errorf("error = %+v", ue)
	}
	g := newFixture(t, nil)
	g.exec.dockerErr = errors.New("daemon down")
	if err := g.up(); err == nil || !strings.Contains(uiErr(t, err).What, "Docker") {
		t.Errorf("docker error = %v", err)
	}
	h := newFixture(t, nil)
	h.exec.dockerErr = errors.New("daemon down")
	h.opts.Deploy.SkipBuild = true
	h.opts.PublicURL = "https://app.example.com"
	writeFile(t, h.root, "spinup.toml", sampleConfig+"\n[env.production]\npublic_url = \"https://app.example.com\"\n")
	if err := h.up(); err != nil {
		t.Errorf("--skip-build must not need docker: %v", err)
	}
}

func TestUpDirtyTree(t *testing.T) {
	f := newFixture(t, nil)
	f.exec.porcelain = " M api/main.go\n"
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.Try, "--allow-dirty") {
		t.Errorf("Try = %q", ue.Try)
	}
	g := newFixture(t, nil)
	g.exec.porcelain = " M spinup.toml\n?? secrets/\n"
	if err := g.up(); err != nil {
		t.Errorf("own files must not make the tree dirty: %v", err)
	}
	h := newFixture(t, nil)
	h.exec.porcelain = " M api/main.go\n"
	h.opts.Deploy.AllowDirty = true
	if err := h.up(); err != nil {
		t.Errorf("--allow-dirty: %v", err)
	}
}

func TestUpNoRecoveryFlagsFailBeforeCost(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.NoRecovery = false
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.Try, "--recovery-recipient age1... or --no-recovery") {
		t.Errorf("Try = %q", ue.Try)
	}
	if len(f.srv.Requests()) != 0 {
		t.Error("called the API")
	}
}

func TestUpNoCommit(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.NoCommit = true
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if len(f.deploys) != 0 || len(f.exec.gitCalls("add")) != 0 {
		t.Error("--no-commit must stop before the deploy")
	}
	if !strings.Contains(f.out.String(), "spinup deploy --env production") {
		t.Errorf("no hint:\n%s", f.out.String())
	}
	g := newFixture(t, nil)
	g.opts.NoCommit, g.opts.Deploy.AllowDirty = true, true
	if err := g.up(); err != nil || len(g.deploys) != 1 {
		t.Errorf("--no-commit with --allow-dirty: err %v, deploys %d", err, len(g.deploys))
	}
}

func TestUpSkipDeploy(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.SkipDeploy = true
	if err := f.up(); err != nil || len(f.deploys) != 0 {
		t.Fatalf("err %v, deploys %d", err, len(f.deploys))
	}
}

func TestUpDomainWithZone(t *testing.T) {
	f := newFixture(t, func(f *fixture) {})
	f.srv.AddDomain("example.com")
	f.opts.Domain = "app.example.com"
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	recs := f.srv.Records("example.com")
	if len(recs) != 1 || recs[0].Type != "A" || recs[0].Name != "app" || recs[0].Data != "203.0.113.10" || recs[0].TTL != 300 {
		t.Fatalf("records = %+v", recs)
	}
	if got := f.config().Envs["production"].PublicURL; got != "https://app.example.com" {
		t.Errorf("public_url = %q", got)
	}
	st, _ := readStored(f.root, "production")
	if st.Domain != "app.example.com" {
		t.Errorf("domain = %q", st.Domain)
	}
	// The second run, without --domain, keeps the domain and sends no write.
	f.opts.Domain = ""
	before := len(f.srv.Requests())
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.srv.Requests()[before:] {
		if r.Method != http.MethodGet {
			t.Errorf("second run sent %s %s", r.Method, r.Path)
		}
	}
	if got := f.config().Envs["production"].PublicURL; got != "https://app.example.com" {
		t.Errorf("public_url after rerun = %q", got)
	}
}

func TestUpDomainWithoutZoneWaitsForResolver(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.Domain = "app.example.org"
	calls := 0
	f.resolve = func(host string) []netip.Addr {
		calls++
		if calls < 3 {
			return nil
		}
		return []netip.Addr{netip.MustParseAddr("203.0.113.10")}
	}
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !strings.Contains(f.out.String(), "Add this DNS record: app.example.org A 203.0.113.10") {
		t.Errorf("calls %d, out:\n%s", calls, f.out.String())
	}
}

func TestUpDomainWithoutZoneTimesOut(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.Domain = "app.example.org"
	f.opts.DNSTimeout = 35 * time.Second
	f.resolve = func(string) []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.0.2.1")} }
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.Try, "app.example.org A 203.0.113.10") {
		t.Errorf("Try = %q", ue.Try)
	}
	if len(f.deploys) != 0 {
		t.Error("deployed before DNS")
	}
}

func TestUpWarnsAboutForeignAAAA(t *testing.T) {
	f := newFixture(t, nil)
	f.srv.AddDomain("example.com")
	f.opts.Domain = "app.example.com"
	if _, _, err := f.cloud.EnsureRecord(context.Background(), "example.com", digitalocean.Record{Type: "AAAA", Name: "app", Data: "2001:db8::99"}); err != nil {
		t.Fatal(err)
	}
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.err.String(), "AAAA record") {
		t.Errorf("no AAAA warning:\n%s", f.err.String())
	}
}

func TestUpAdoptsExistingDropletWithoutNewHostKey(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	keys := f.config().Envs["production"].HostKeys
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if got := f.config().Envs["production"].HostKeys; len(got) != 1 || got[0] != keys[0] || f.exec.keygens != 1 {
		t.Errorf("host keys changed: %v, keygens %d", got, f.exec.keygens)
	}
}

func TestUpAdoptWithoutPinFails(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if err := editConfig(f.root, []edit{del([]string{"env", "production"}, "host_keys")}); err != nil {
		t.Fatal(err)
	}
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.Try, "--recreate") {
		t.Errorf("Try = %q", ue.Try)
	}
}

func TestUpDropletIDMismatch(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if err := editConfig(f.root, []edit{set([]string{"env", "production", "digitalocean"}, "droplet_id", int64(42))}); err != nil {
		t.Fatal(err)
	}
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.Why, "42") {
		t.Errorf("error = %+v", ue)
	}
}

func TestUpSeveralDroplets(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cloud.CreateDroplet(context.Background(), digitalocean.DropletRequest{Name: "x", Region: "nyc3", Size: "s-1vcpu-2gb", Image: "ubuntu-24-04-x64", Tags: []string{"spinup:demo:production"}}); err != nil {
		t.Fatal(err)
	}
	ue := uiErr(t, f.up())
	ds := f.srv.Droplets()
	if len(ds) != 2 || !strings.Contains(ue.Why, strconv.Itoa(ds[0].ID)) || !strings.Contains(ue.Why, strconv.Itoa(ds[1].ID)) {
		t.Errorf("error = %+v", ue)
	}
}

func TestUpRecreate(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	oldKey := f.config().Envs["production"].HostKeys[0]
	oldID := f.srv.Droplets()[0].ID
	// A release exists: --recreate needs --i-know.
	f.vm.handler = func(argv []string) (string, string, int, error) {
		if argv[0] == "sh" {
			return `[{"release":"r1","git_sha":"abc"}]`, "", 0, nil
		}
		return "", "", 0, nil
	}
	f.opts.Recreate = true
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.Try, "--i-know") {
		t.Errorf("Try = %q", ue.Try)
	}
	if len(f.srv.Droplets()) != 1 || f.srv.Droplets()[0].ID != oldID {
		t.Fatalf("recreate without --i-know changed droplets: %+v", f.srv.Droplets())
	}
	f.opts.IKnow = true
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	ds := f.srv.Droplets()
	if len(ds) != 1 || ds[0].ID == oldID {
		t.Fatalf("droplets = %+v", ds)
	}
	if newKey := f.config().Envs["production"].HostKeys[0]; newKey == oldKey {
		t.Error("recreate kept the old host key")
	}
}

func TestUpRecreateWithoutReleaseNeedsNoIKnow(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	oldID := f.srv.Droplets()[0].ID
	f.opts.Recreate = true
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if ds := f.srv.Droplets(); len(ds) != 1 || ds[0].ID == oldID {
		t.Fatalf("droplets = %+v", ds)
	}
}

func TestUpDialErrorIsFatal(t *testing.T) {
	f := newFixture(t, nil)
	f.dialErr = errors.New("bad host")
	ue := uiErr(t, f.up())
	if !strings.Contains(ue.Why, "bad host") {
		t.Errorf("error = %+v", ue)
	}
}

func TestUpPassesDeployFlags(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.Deploy.AllowDestructive = true
	f.opts.Deploy.Tag = "abc"
	f.exec.staged = "spinup.toml\n"
	// The first run commits, so HEAD moves and the tag cannot match it.
	if ue := uiErr(t, f.up()); !strings.Contains(ue.Try, "--skip-deploy") || len(f.deploys) != 0 {
		t.Fatalf("first run with --tag: %+v, deploys = %d", ue, len(f.deploys))
	}
	// The files are committed now, so the second run deploys the tag.
	f.exec.staged = ""
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if o := f.deploys[0]; !o.AllowDestructive || o.Tag != "abc" || o.Out != f.out || o.ErrOut != f.err {
		t.Errorf("deploy options = %+v", o)
	}
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, root, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
