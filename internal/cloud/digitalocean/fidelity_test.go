package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean/dofake"
)

func want422(t *testing.T, err error) {
	t.Helper()
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnprocessableEntity {
		t.Fatalf("error = %v, want HTTP 422", err)
	}
}

func TestFakeTagNameRules(t *testing.T) {
	ctx := context.Background()
	c, _, _ := newFake(t, dofake.Options{})
	for _, bad := range []string{"has space", "a/b", "dot.name", strings.Repeat("a", 256)} {
		_, err := c.EnsureTag(ctx, bad)
		want422(t, err)
	}
	for _, good := range []string{"spinup:demo:production", "a_b-c", strings.Repeat("a", 255)} {
		if _, err := c.EnsureTag(ctx, good); err != nil {
			t.Fatalf("tag %q: %v", good, err)
		}
	}
}

func TestFakeDuplicateTagIsRefused(t *testing.T) {
	ctx := context.Background()
	c, _, _ := newFake(t, dofake.Options{})
	if err := c.do(ctx, http.MethodPost, "/v2/tags", Tag{Name: "spinup"}, nil); err != nil {
		t.Fatal(err)
	}
	want422(t, c.do(ctx, http.MethodPost, "/v2/tags", Tag{Name: "spinup"}, nil))
	// EnsureTag still treats the lost race as success.
	if created, err := c.EnsureTag(ctx, "spinup"); err != nil || created {
		t.Fatalf("ensure: %v %v", created, err)
	}
}

func TestFakeDropletNameAndTagRules(t *testing.T) {
	ctx := context.Background()
	c, _, _ := newFake(t, dofake.Options{})
	req := DropletRequest{Name: "app-prod", Region: "nyc3", Size: "s-2vcpu-4gb", Image: "ubuntu-24-04-x64"}
	for _, name := range []string{"app-prod-", "app prod", "app_prod"} {
		req.Name = name
		_, err := c.CreateDroplet(ctx, req)
		want422(t, err)
	}
	req.Name = "app-prod"
	req.Tags = []string{"bad tag"}
	_, err := c.CreateDroplet(ctx, req)
	want422(t, err)
}

func TestFakeFirewallRules(t *testing.T) {
	ctx := context.Background()
	c, f, _ := newFake(t, dofake.Options{})
	post := func(name string, in []InboundRule) error {
		return c.do(ctx, http.MethodPost, "/v2/firewalls", Firewall{Name: name, InboundRules: in}, nil)
	}
	all := Sources{Addresses: []string{"0.0.0.0/0"}}
	want422(t, post("bad_name", nil))
	want422(t, post("-bad", nil))
	want422(t, post("ok", []InboundRule{{Protocol: "sctp", Ports: "22", Sources: all}}))
	want422(t, post("ok", []InboundRule{{Protocol: "tcp", Ports: "ssh", Sources: all}}))
	if err := post("ok", []InboundRule{{Protocol: "tcp", Ports: "8000-9000", Sources: all}}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Firewalls()); n != 1 {
		t.Fatalf("firewalls = %d, want 1", n)
	}
}

func TestFakeFirewallPortsAndStatus(t *testing.T) {
	ctx := context.Background()
	c, _, _ := newFake(t, dofake.Options{})
	want := spinupFirewall("0.0.0.0/0", "spinup:app:prod")
	want.OutboundRules = append(want.OutboundRules, OutboundRule{Protocol: "icmp", Destinations: Destinations{Addresses: []string{"0.0.0.0/0"}}})
	fw, _, err := c.EnsureFirewall(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	if fw.Status != "waiting" {
		t.Fatalf("status after create = %q, want waiting", fw.Status)
	}
	// The first read still shows the pending change. The next read shows the result.
	all, err := c.Firewalls(ctx)
	if err != nil || len(all) != 1 || all[0].Status != "waiting" {
		t.Fatalf("first read: %+v %v", all, err)
	}
	all, _ = c.Firewalls(ctx)
	if all[0].Status != "succeeded" {
		t.Fatalf("second read status = %q, want succeeded", all[0].Status)
	}
	// All ports and ICMP answer "0".
	for _, r := range all[0].OutboundRules {
		if r.Ports != "0" {
			t.Fatalf("outbound %s ports = %q, want 0", r.Protocol, r.Ports)
		}
	}
	// The answer still counts as the same firewall, so a second run changes nothing.
	if _, changed, err := c.EnsureFirewall(ctx, want); err != nil || changed {
		t.Fatalf("second ensure: changed=%v err=%v", changed, err)
	}
}

func TestFakeErrorBodyHasRequestID(t *testing.T) {
	ctx := context.Background()
	c, _, _ := newFake(t, dofake.Options{})
	_, err := c.Droplet(ctx, 1)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound || ae.RequestID == "" {
		t.Fatalf("error = %v, want HTTP 404 with a request ID", err)
	}
	// The body carries the ID too, so the client reads it without the header.
	var fromBody *APIError
	if !errors.As(parseError(404, http.Header{}, []byte(`{"id":"not_found","message":"x","request_id":"abc"}`), "GET", "/v2/x"), &fromBody) || fromBody.RequestID != "abc" {
		t.Fatalf("request ID from body = %+v", fromBody)
	}
}

func TestFakeRateLimitHeaders(t *testing.T) {
	c, _, _ := newFake(t, dofake.Options{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, c.Base+"/v2/account", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, h := range []string{"ratelimit-limit", "ratelimit-remaining", "ratelimit-reset"} {
		if resp.Header.Get(h) == "" {
			t.Fatalf("missing header %s", h)
		}
	}
}
