package provision

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestMakeValuesGeneratesOnlyMissing(t *testing.T) {
	got, err := makeValues([]string{"POSTGRES_PASSWORD", "DATABASE_URL", "GRAFANA_ADMIN_PASSWORD"}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	pw := got["POSTGRES_PASSWORD"]
	if len(pw) != 43 || got["DATABASE_URL"] != "postgres://app:"+pw+"@postgres:5432/app?sslmode=disable" || got["GRAFANA_ADMIN_PASSWORD"] == "" || got["GRAFANA_ADMIN_PASSWORD"] == pw {
		t.Errorf("values = %v", got)
	}
}

func TestMakeValuesKeepsPostgresPassword(t *testing.T) {
	got, err := makeValues([]string{"DATABASE_URL"}, map[string]string{"POSTGRES_PASSWORD": "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["DATABASE_URL"] != "postgres://app:kept@postgres:5432/app?sslmode=disable" {
		t.Errorf("values = %v", got)
	}
}

func TestMakeValuesDerivesPasswordFromURL(t *testing.T) {
	have := map[string]string{"DATABASE_URL": "postgres://app:fromurl@postgres:5432/app?sslmode=disable"}
	got, err := makeValues([]string{"POSTGRES_PASSWORD"}, have)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["POSTGRES_PASSWORD"] != "fromurl" {
		t.Errorf("values = %v", got)
	}
	have["DATABASE_URL"] = "postgres://other:x@db/app"
	_, err = makeValues([]string{"POSTGRES_PASSWORD"}, have)
	if ue := uiErr(t, err); !strings.Contains(ue.Why, "not app") {
		t.Errorf("error = %+v", ue)
	}
}

func TestUpSecretsKeepExistingValues(t *testing.T) {
	f := newFixture(t, nil)
	f.sec.exists = true
	f.sec.vals = map[string]string{"POSTGRES_PASSWORD": "kept"}
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if len(f.sec.inits) != 0 {
		t.Error("initialized existing secrets")
	}
	if len(f.sec.sets) != 1 || f.sec.sets[0] != "DATABASE_URL" || f.sec.vals["POSTGRES_PASSWORD"] != "kept" {
		t.Errorf("sets = %v, vals = %v", f.sec.sets, f.sec.vals)
	}
	if strings.Contains(f.out.String(), f.sec.vals["DATABASE_URL"]) || !strings.Contains(f.out.String(), "Made the secrets: DATABASE_URL") {
		t.Errorf("output:\n%s", f.out.String())
	}
}

func TestUpSecretsFromURLOnly(t *testing.T) {
	f := newFixture(t, nil)
	f.sec.exists = true
	f.sec.vals = map[string]string{"DATABASE_URL": "postgres://app:abc@postgres:5432/app?sslmode=disable"}
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if f.sec.vals["POSTGRES_PASSWORD"] != "abc" || len(f.sec.sets) != 1 {
		t.Errorf("sets = %v, vals = %v", f.sec.sets, f.sec.vals)
	}
}

func TestUpSecretsInitWithRecipient(t *testing.T) {
	f := newFixture(t, nil)
	f.opts.NoRecovery, f.opts.RecoveryRecipient = false, "age1abc"
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if len(f.sec.inits) != 1 || f.sec.inits[0].RecoveryRecipient != "age1abc" || f.sec.inits[0].Env != "production" {
		t.Errorf("inits = %+v", f.sec.inits)
	}
}

func provisioned(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	f.out.Reset()
	f.err.Reset()
	return f
}

func TestStatusJSON(t *testing.T) {
	f := provisioned(t)
	f.opts.JSON = true
	if err := Status(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(f.out.Bytes(), &raw); err != nil {
		t.Fatalf("%v:\n%s", err, f.out.String())
	}
	for _, k := range []string{"env", "account", "config", "droplet", "firewall", "drift"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("JSON lacks %q:\n%s", k, f.out.String())
		}
	}
	var rep Report
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Account.Email != "dev@example.com" || rep.Account.Team != "team-1" || rep.Account.TeamName != "Team One" || rep.Droplet.IPv4 != "203.0.113.10" || rep.Droplet.Status != "active" || !rep.Firewall.Found || len(rep.Drift) != 0 || rep.Config.HostKeys != 1 {
		t.Errorf("report = %+v", rep)
	}
}

func TestStatusShowsTeamNameAndUUID(t *testing.T) {
	f := provisioned(t)
	if err := Status(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "Account: dev@example.com, team Team One (team-1)\n") {
		t.Errorf("status = %q", f.out.String())
	}
	if got := TeamLabel("", "team-1"); got != "team-1" {
		t.Errorf("TeamLabel without a name = %q", got)
	}
}

func TestStatusReadOnlyAndDrift(t *testing.T) {
	f := provisioned(t)
	f.srv.AddDomain("example.com")
	if err := editConfig(f.root, []edit{
		set([]string{"env", "production"}, "host", "192.0.2.9"),
		set([]string{"env", "production", "digitalocean"}, "droplet_id", int64(7)),
		set([]string{"env", "production", "digitalocean"}, "firewall_id", "fw-other"),
		set([]string{"env", "production", "digitalocean"}, "domain", "app.example.com"),
	}); err != nil {
		t.Fatal(err)
	}
	before := len(f.srv.Requests())
	if err := Status(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.srv.Requests()[before:] {
		if r.Method != http.MethodGet {
			t.Errorf("status sent %s %s", r.Method, r.Path)
		}
	}
	out := f.out.String()
	for _, want := range []string{"droplet_id is 7", "host is 192.0.2.9", "firewall_id is fw-other", "no A record"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestStatusMissingFirewall(t *testing.T) {
	f := provisioned(t)
	st, _ := readStored(f.root, "production")
	if err := f.cloud.DeleteFirewall(context.Background(), st.FirewallID); err != nil {
		t.Fatal(err)
	}
	f.opts.JSON = true
	if err := Status(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Firewall.Found || len(rep.Drift) != 1 || !strings.Contains(rep.Drift[0], "firewall") {
		t.Errorf("report = %+v", rep)
	}
}

func TestDestroyRequiresIKnow(t *testing.T) {
	f := provisioned(t)
	ue := uiErr(t, Destroy(context.Background(), f.opts, f.deps))
	if !strings.Contains(ue.Try, "--i-know") {
		t.Errorf("Try = %q", ue.Try)
	}
	if len(f.srv.Droplets()) != 1 {
		t.Error("deleted without --i-know")
	}
}

func TestDestroyNeedsTypedName(t *testing.T) {
	f := provisioned(t)
	f.opts.IKnow = true
	f.opts.Confirm = project.Confirm{Interactive: true, In: strings.NewReader("staging\n"), Out: &strings.Builder{}}
	if err := Destroy(context.Background(), f.opts, f.deps); err == nil {
		t.Fatal("want a confirmation error")
	}
	if len(f.srv.Droplets()) != 1 {
		t.Error("deleted after a wrong name")
	}
	f.opts.Confirm = project.Confirm{Yes: true}
	if err := Destroy(context.Background(), f.opts, f.deps); err == nil {
		t.Error("--yes without CI must not skip the confirmation")
	}
}

func TestDestroyDeletesOnlyMatchingResources(t *testing.T) {
	f := newFixture(t, nil)
	f.srv.AddDomain("example.com")
	f.opts.Domain = "app.example.com"
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	f.opts.Domain = ""
	f.out.Reset()
	f.err.Reset()
	oldID := f.srv.Droplets()[0].ID
	// A second droplet with the same tag must stay.
	if _, err := f.cloud.CreateDroplet(context.Background(), digitalocean2Req()); err != nil {
		t.Fatal(err)
	}
	f.opts.IKnow = true
	f.opts.Confirm = project.Confirm{Interactive: true, In: strings.NewReader("production\n"), Out: &strings.Builder{}}
	if err := Destroy(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	ds := f.srv.Droplets()
	if len(ds) != 1 || ds[0].ID == oldID {
		t.Fatalf("droplets = %+v", ds)
	}
	if len(f.srv.Firewalls()) != 0 {
		t.Error("firewall stays")
	}
	if len(f.srv.Records("example.com")) != 0 {
		t.Errorf("records = %+v", f.srv.Records("example.com"))
	}
	if len(f.srv.SSHKeys()) != 1 {
		t.Error("deleted the SSH key")
	}
	// The second droplet still has the tags, so they stay.
	if tags := f.srv.Tags(); len(tags) != 2 {
		t.Errorf("tags = %v, want the environment tag and spinup", tags)
	}
	for _, want := range []string{"database on the droplet is lost", "spinup backup", "Not deleted"} {
		if !strings.Contains(f.err.String(), want) {
			t.Errorf("ErrOut lacks %q:\n%s", want, f.err.String())
		}
	}
	env := f.config().Envs["production"]
	if env.Host != "" || len(env.HostKeys) != 0 || env.PublicURL != "https://app.example.com" {
		// A custom domain URL stays. Only an sslip URL goes.
		t.Errorf("env = %+v", env)
	}
	st, _ := readStored(f.root, "production")
	if st.DropletID != 0 || st.FirewallID != "" || st.Region != "nyc3" || st.Size != "s-2vcpu-4gb" || st.Tag == "" || st.Team != "team-1" || st.Domain != "app.example.com" {
		t.Errorf("stored = %+v", st)
	}
	text := f.configText()
	if !strings.Contains(text, "# The staging environment stays as it is.") || !strings.Contains(text, `host = "198.51.100.5"`) {
		t.Errorf("staging section changed:\n%s", text)
	}
}

func TestDestroyRemovesSslipURLThenUpProvisionsAgain(t *testing.T) {
	f := provisioned(t)
	f.opts.IKnow = true
	f.opts.Confirm = project.Confirm{Yes: true, CI: true}
	if err := Destroy(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	if env := f.config().Envs["production"]; env.PublicURL != "" || env.Host != "" {
		t.Errorf("env = %+v", env)
	}
	if len(f.srv.Droplets()) != 0 || len(f.srv.Firewalls()) != 0 {
		t.Error("resources remain")
	}
	f.opts.IKnow = false
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	if len(f.srv.Droplets()) != 1 {
		t.Errorf("droplets = %+v", f.srv.Droplets())
	}
}

func TestDestroyKeepsRecordThatPointsElsewhere(t *testing.T) {
	f := provisioned(t)
	f.srv.AddDomain("example.com")
	if err := editConfig(f.root, []edit{set([]string{"env", "production", "digitalocean"}, "domain", "app.example.com")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.cloud.EnsureRecord(context.Background(), "example.com", digitalocean2Rec("192.0.2.77")); err != nil {
		t.Fatal(err)
	}
	f.opts.IKnow = true
	f.opts.Confirm = project.Confirm{Yes: true, CI: true}
	if err := Destroy(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	if len(f.srv.Records("example.com")) != 1 {
		t.Error("deleted a record that points elsewhere")
	}
}

func TestDestroyTeamMismatch(t *testing.T) {
	f := provisioned(t)
	other := newFixture(t, func(o *fixture) { o.fakeOpts.TeamUUID = "team-2" })
	writeFile(t, other.root, "spinup.toml", f.configText())
	other.opts.IKnow = true
	other.opts.Confirm = project.Confirm{Yes: true, CI: true}
	err := Destroy(context.Background(), other.opts, other.deps)
	if ue := uiErr(t, err); !strings.Contains(ue.Why, "team-1") {
		t.Errorf("error = %+v", ue)
	}
}

func TestDatabaseURLEscapesThePassword(t *testing.T) {
	for _, pw := range []string{"plain", "p@ss/w:rd#1", "a b?c%d"} {
		got, err := passwordFromURL(databaseURL(pw))
		if err != nil || got != pw {
			t.Errorf("round trip of %q: got %q, %v (url %s)", pw, got, err, databaseURL(pw))
		}
	}
	if got := databaseURL("abc"); got != "postgres://app:abc@postgres:5432/app?sslmode=disable" {
		t.Errorf("databaseURL = %s", got)
	}
}

func TestDestroyAfterAnUpThatStoppedEarly(t *testing.T) {
	// The droplet exists, but the run stops before the droplet is active.
	f := newFixture(t, nil)
	f.srv.FailNext("GET", "/v2/droplets/", 404, 100)
	if err := f.up(); err == nil {
		t.Fatal("up must fail while the droplet cannot be read")
	}
	st, _ := readStored(f.root, "production")
	if st.DropletID == 0 || st.FirewallID == "" {
		t.Fatalf("the IDs must be in spinup.toml at once: %+v", st)
	}
	f.srv.FailNext("GET", "/v2/droplets/", 404, 0)
	f.opts.IKnow = true
	f.opts.Confirm = project.Confirm{Yes: true, CI: true}
	if err := Destroy(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	if len(f.srv.Droplets()) != 0 || len(f.srv.Firewalls()) != 0 {
		t.Errorf("droplets = %d, firewalls = %d, want 0 and 0", len(f.srv.Droplets()), len(f.srv.Firewalls()))
	}
	if tags := f.srv.Tags(); len(tags) != 0 {
		t.Errorf("tags = %v, want none: destroy deletes the empty tags", tags)
	}

	// The create fails after the firewall exists. spinup.toml has no firewall_id.
	g := newFixture(t, nil)
	g.srv.FailNext("POST", "/v2/droplets", 422, 1)
	if err := g.up(); err == nil {
		t.Fatal("up must fail when the create fails")
	}
	if st, _ := readStored(g.root, "production"); st.FirewallID != "" {
		t.Fatalf("firewall_id = %q, want none", st.FirewallID)
	}
	g.opts.IKnow = true
	g.opts.Confirm = project.Confirm{Yes: true, CI: true}
	if err := Destroy(context.Background(), g.opts, g.deps); err != nil {
		t.Fatal(err)
	}
	if len(g.srv.Firewalls()) != 0 {
		t.Error("destroy must delete the firewall of the environment by its name and tag")
	}
}

func TestDestroyKeepsTagsWithoutTheDeleteScope(t *testing.T) {
	// A token from before the tag:delete scope gets HTTP 403. Destroy warns and finishes.
	f := newFixture(t, nil)
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
	f.srv.FailNext("DELETE", "/v2/tags/", 403, 2)
	f.opts.IKnow = true
	f.opts.Confirm = project.Confirm{Yes: true, CI: true}
	if err := Destroy(context.Background(), f.opts, f.deps); err != nil {
		t.Fatal(err)
	}
	if len(f.srv.Droplets()) != 0 || len(f.srv.Tags()) != 2 {
		t.Errorf("droplets = %d, tags = %v", len(f.srv.Droplets()), f.srv.Tags())
	}
	if !strings.Contains(f.err.String(), "tag:delete") {
		t.Errorf("ErrOut lacks the scope hint:\n%s", f.err.String())
	}
}

func TestFailWrapKeepsOneTryLine(t *testing.T) {
	var ue *ui.Error
	err := failWrap("confirm the destroy", errors.New(`cannot destroy in protected environment "staging". Try: re-run in a terminal`), "type the environment name")
	if !errors.As(err, &ue) || strings.Contains(ue.Why, "Try:") || ue.Try != "type the environment name" {
		t.Fatalf("a plain error: %+v", ue)
	}
	api := &digitalocean.APIError{Status: 403, Method: "GET", Path: "/v2/x"}
	err = failWrap("read x", api, "run the command again")
	if !errors.As(err, &ue) || strings.Contains(ue.Why, "Try:") || "Try: "+ue.Try != api.Try() {
		t.Errorf("an API error keeps its own Try line: %+v", ue)
	}
}
