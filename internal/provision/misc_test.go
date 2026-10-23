package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/project"
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
	if rep.Account.Email != "dev@example.com" || rep.Account.Team != "team-1" || rep.Droplet.IPv4 != "203.0.113.10" || rep.Droplet.Status != "active" || !rep.Firewall.Found || len(rep.Drift) != 0 || rep.Config.HostKeys != 1 {
		t.Errorf("report = %+v", rep)
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
