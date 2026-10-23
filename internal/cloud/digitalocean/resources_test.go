package digitalocean

import (
	"context"
	"crypto/ed25519"
	"crypto/md5" //nolint:gosec // test of the MD5 fingerprint.
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean/dofake"
)

// testKey returns a fixed ed25519 public key line and its blob.
func testKey(seed byte) (line string, blob []byte) {
	priv := ed25519.NewKeyFromSeed([]byte(strings.Repeat(string([]byte{seed}), 32)))
	pub := priv.Public().(ed25519.PublicKey)
	for _, part := range [][]byte{[]byte("ssh-ed25519"), pub} {
		blob = binary.BigEndian.AppendUint32(blob, uint32(len(part)))
		blob = append(blob, part...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " test@host", blob
}

func TestFingerprint(t *testing.T) {
	line, blob := testKey(7)
	sum := md5.Sum(blob) //nolint:gosec // see the import.
	var parts []string
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02x", b))
	}
	want := strings.Join(parts, ":")
	got, err := Fingerprint(line)
	if err != nil || got != want {
		t.Fatalf("Fingerprint = %q, %v; want %q", got, err, want)
	}
	for _, bad := range []string{"", "ssh-ed25519", "ssh-ed25519 !!!"} {
		if _, err := Fingerprint(bad); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("Fingerprint(%q) err = %v", bad, err)
		}
	}
}

func TestEnsureSSHKey(t *testing.T) {
	ctx := context.Background()
	c, f, _ := newFake(t, dofake.Options{})
	line, _ := testKey(1)
	k, created, err := c.EnsureSSHKey(ctx, "spinup", line)
	if err != nil || !created || k.Fingerprint == "" || k.ID == 0 {
		t.Fatalf("create: %+v %v %v", k, created, err)
	}
	k2, created, err := c.EnsureSSHKey(ctx, "spinup", line)
	if err != nil || created || k2.ID != k.ID {
		t.Fatalf("adopt: %+v %v %v", k2, created, err)
	}
	if len(f.SSHKeys()) != 1 {
		t.Fatalf("keys = %v", f.SSHKeys())
	}
	for _, r := range f.Requests() {
		if r.Method == http.MethodGet && r.Path == "/v2/account/keys" {
			t.Fatal("the list endpoint was used")
		}
	}
	got, err := c.SSHKeyByFingerprint(ctx, "00:11")
	if got != nil || err != nil {
		t.Fatalf("missing key: %v %v", got, err)
	}
}

func TestEnsureSSHKeyRace(t *testing.T) {
	line, _ := testKey(2)
	fp, _ := Fingerprint(line)
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			if gets == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"ssh_key":{"id":9,"fingerprint":%q,"name":"other"}}`, fp)
		default:
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"id":"unprocessable_entity","message":"SSH Key is already in use on your account"}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, func(context.Context) (string, error) { return testToken, nil })
	k, created, err := c.EnsureSSHKey(context.Background(), "spinup", line)
	if err != nil || created || k.ID != 9 {
		t.Fatalf("race: %+v %v %v", k, created, err)
	}
}

func TestEnsureSSHKeyDuplicateNameStaysError(t *testing.T) {
	line, _ := testKey(3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"id":"unprocessable_entity","message":"Name is already in use"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, func(context.Context) (string, error) { return testToken, nil })
	_, _, err := c.EnsureSSHKey(context.Background(), "spinup", line)
	assertAPIError(t, err, 422, "status.digitalocean.com")
}

func newDroplet(t *testing.T, c *Client, line string) Droplet {
	t.Helper()
	k, _, err := c.EnsureSSHKey(context.Background(), "spinup", line)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.CreateDroplet(context.Background(), DropletRequest{
		Name: "app-prod", Region: "nyc3", Size: "s-2vcpu-4gb", Image: "ubuntu-24-04-x64",
		SSHKeys: []string{k.Fingerprint}, Tags: []string{"spinup", "spinup:app:prod"}, UserData: "#cloud-config\n", IPv6: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDropletLifecycle(t *testing.T) {
	ctx := context.Background()
	c, f, sl := newFake(t, dofake.Options{ActivateAfter: 3})
	line, _ := testKey(4)
	d := newDroplet(t, c, line)
	if d.Status != "new" || d.PublicIPv4() != "" {
		t.Fatalf("new droplet = %+v", d)
	}
	got, err := c.WaitActive(ctx, d.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicIPv4() != "203.0.113.10" || got.PublicIPv6() != "2001:db8::10" || got.SizeSlug != "s-2vcpu-4gb" || got.Region.Slug != "nyc3" {
		t.Fatalf("active droplet = %+v", got)
	}
	if len(sl.waits) != 2 || sl.waits[0] != 5*time.Second {
		t.Fatalf("waits = %v", sl.waits)
	}
	list, err := c.DropletsByTag(ctx, "spinup:app:prod")
	if err != nil || len(list) != 1 || list[0].ID != d.ID {
		t.Fatalf("by tag = %v %v", list, err)
	}
	if list, _ = c.DropletsByTag(ctx, "other"); len(list) != 0 {
		t.Fatalf("other tag = %v", list)
	}
	if ds := f.Droplets(); len(ds) != 1 || ds[0].UserData != "#cloud-config\n" || !ds[0].IPv6 {
		t.Fatalf("fake droplets = %+v", ds)
	}
	if err := c.DeleteDroplet(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDroplet(ctx, d.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestWaitActiveTimeout(t *testing.T) {
	c, _, _ := newFake(t, dofake.Options{ActivateAfter: 1000})
	c.Sleep = sleepCtx // Real sleep, which the timeout stops.
	line, _ := testKey(5)
	d := newDroplet(t, c, line)
	_, err := c.WaitActive(context.Background(), d.ID, 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not active") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("err = %v", err)
	}
}

func TestOnCreateDropletFails(t *testing.T) {
	c, _, _ := newFake(t, dofake.Options{OnCreateDroplet: func(map[string]any) error { return fmt.Errorf("no capacity") }})
	_, err := c.CreateDroplet(context.Background(), DropletRequest{Name: "x", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "i"})
	assertAPIError(t, err, 422, "status.digitalocean.com")
}

func TestFindZone(t *testing.T) {
	c, f, _ := newFake(t, dofake.Options{})
	f.AddDomain("example.com")
	f.AddDomain("sub.example.com")
	f.AddDomain("notexample.com")
	tests := []struct {
		fqdn, zone, name string
		found            bool
	}{
		{"app.example.com", "example.com", "app", true},
		{"example.com", "example.com", "@", true},
		{"a.b.example.com", "example.com", "a.b", true},
		{"x.sub.example.com", "sub.example.com", "x", true},
		{"sub.example.com", "sub.example.com", "@", true},
		{"App.Example.COM.", "example.com", "app", true},
		{"badexample.com", "", "", false},
		{"example.org", "", "", false},
	}
	for _, tt := range tests {
		zone, name, found, err := c.FindZone(context.Background(), tt.fqdn)
		if err != nil || zone != tt.zone || name != tt.name || found != tt.found {
			t.Errorf("FindZone(%q) = %q %q %v %v; want %q %q %v", tt.fqdn, zone, name, found, err, tt.zone, tt.name, tt.found)
		}
	}
}

func TestEnsureRecord(t *testing.T) {
	ctx := context.Background()
	c, f, _ := newFake(t, dofake.Options{})
	f.AddDomain("example.com")
	want := Record{Name: "app", Data: "203.0.113.10"}

	r, changed, err := c.EnsureRecord(ctx, "example.com", want)
	if err != nil || !changed || r.ID == 0 || r.TTL != 300 || r.Type != "A" {
		t.Fatalf("create: %+v %v %v", r, changed, err)
	}
	r2, changed, err := c.EnsureRecord(ctx, "example.com", want)
	if err != nil || changed || r2.ID != r.ID {
		t.Fatalf("no-op: %+v %v %v", r2, changed, err)
	}
	want.Data = "203.0.113.11"
	r3, changed, err := c.EnsureRecord(ctx, "example.com", want)
	if err != nil || !changed || r3.ID != r.ID || r3.Data != "203.0.113.11" {
		t.Fatalf("update: %+v %v %v", r3, changed, err)
	}
	// The apex and another name stay separate.
	if _, _, err := c.EnsureRecord(ctx, "example.com", Record{Name: "@", Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	recs := f.Records("example.com")
	if len(recs) != 2 || recs[0].Data != "203.0.113.11" {
		t.Fatalf("fake records = %+v", recs)
	}
	got, err := c.Records(ctx, "example.com", "A", "@")
	if err != nil || len(got) != 1 || got[0].Name != "@" {
		t.Fatalf("apex filter = %+v %v", got, err)
	}
	// Two A records for one name is an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"domain_records":[{"id":1,"type":"A","name":"app","data":"1.1.1.1","ttl":300},{"id":2,"type":"A","name":"app","data":"2.2.2.2","ttl":300}]}`)
	}))
	defer srv.Close()
	dup := New(srv.URL, func(context.Context) (string, error) { return testToken, nil })
	_, _, err = dup.EnsureRecord(ctx, "example.com", want)
	if err == nil || !strings.Contains(err.Error(), "2 A records") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("duplicate: %v", err)
	}
	if err := c.DeleteRecord(ctx, "example.com", r.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRecord(ctx, "example.com", r.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func spinupFirewall(sshFrom string, tags ...string) Firewall {
	all := []string{"0.0.0.0/0", "::/0"}
	return Firewall{
		Name: "spinup-app-prod", Tags: tags,
		InboundRules: []InboundRule{
			{Protocol: "tcp", Ports: "22", Sources: Sources{Addresses: []string{sshFrom}}},
			{Protocol: "tcp", Ports: "80", Sources: Sources{Addresses: all}},
			{Protocol: "tcp", Ports: "443", Sources: Sources{Addresses: all}},
			{Protocol: "udp", Ports: "443", Sources: Sources{Addresses: all}},
		},
		OutboundRules: []OutboundRule{
			{Protocol: "tcp", Ports: "all", Destinations: Destinations{Addresses: all}},
			{Protocol: "udp", Ports: "all", Destinations: Destinations{Addresses: all}},
		},
	}
}

func TestEnsureFirewall(t *testing.T) {
	ctx := context.Background()
	c, f, _ := newFake(t, dofake.Options{})
	want := spinupFirewall("0.0.0.0/0", "spinup:app:prod")
	want.DropletIDs = []int{77}

	fw, changed, err := c.EnsureFirewall(ctx, want)
	if err != nil || !changed || fw.ID == "" {
		t.Fatalf("create: %+v %v %v", fw, changed, err)
	}
	// Same rules in a different order and "0" for all ports: no change.
	again := spinupFirewall("0.0.0.0/0", "spinup:app:prod")
	again.InboundRules[0], again.InboundRules[3] = again.InboundRules[3], again.InboundRules[0]
	again.OutboundRules[0].Ports = "0"
	fw2, changed, err := c.EnsureFirewall(ctx, again)
	if err != nil || changed || fw2.ID != fw.ID {
		t.Fatalf("no-op: %+v %v %v", fw2, changed, err)
	}
	// A new ssh source is drift. The droplet IDs stay.
	drift := spinupFirewall("203.0.113.5/32", "spinup:app:prod")
	fw3, changed, err := c.EnsureFirewall(ctx, drift)
	if err != nil || !changed || fw3.ID != fw.ID {
		t.Fatalf("drift: %+v %v %v", fw3, changed, err)
	}
	fws := f.Firewalls()
	if len(fws) != 1 || !reflect.DeepEqual(fws[0]["droplet_ids"], []any{77.0}) {
		t.Fatalf("fake firewalls = %+v", fws)
	}
	if !strings.Contains(fmt.Sprint(fws[0]["inbound_rules"]), "203.0.113.5/32") {
		t.Fatalf("rules not updated: %+v", fws[0])
	}
	if err := c.DeleteFirewall(ctx, fw.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteFirewall(ctx, fw.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAccountAndCatalog(t *testing.T) {
	ctx := context.Background()
	c, _, _ := newFake(t, dofake.Options{TeamUUID: "team-1", TeamName: "Acme"})
	acc, err := c.Account(ctx)
	if err != nil || acc.Team == nil || acc.Team.UUID != "team-1" || acc.DropletLimit != 10 || acc.Email != "dev@example.com" {
		t.Fatalf("account = %+v %v", acc, err)
	}
	sizes, err := c.Sizes(ctx)
	if err != nil || len(sizes) < 4 {
		t.Fatalf("sizes = %v %v", sizes, err)
	}
	var s Size
	for _, z := range sizes {
		if z.Slug == "s-2vcpu-4gb" {
			s = z
		}
	}
	if s.VCPUs != 2 || s.Memory != 4096 || s.PriceMonthly != 24 || s.PriceHourly <= 0 || !s.Available {
		t.Fatalf("size = %+v", s)
	}
	regions, err := c.Regions(ctx)
	if err != nil || len(regions) != 3 || len(regions[0].Sizes) == 0 {
		t.Fatalf("regions = %v %v", regions, err)
	}
}
