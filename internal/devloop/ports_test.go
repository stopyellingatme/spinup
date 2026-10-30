package devloop

import (
	"bytes"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
)

type portListener struct {
	port   int
	closed bool
}

func (l *portListener) Accept() (net.Conn, error) { return nil, errors.New("not supported") }
func (l *portListener) Close() error              { l.closed = true; return nil }
func (l *portListener) Addr() net.Addr            { return &net.TCPAddr{Port: l.port} }

func TestFreePort(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		preferred  int
		busy, fail bool
		want       int
		addresses  []string
	}{
		{name: "preferred", preferred: 8080, want: 8080, addresses: []string{"127.0.0.1:8080"}},
		{name: "busy", preferred: 8080, busy: true, want: 34567, addresses: []string{"127.0.0.1:8080", "127.0.0.1:0"}},
		{name: "ephemeral", want: 34567, addresses: []string{"127.0.0.1:0"}},
		{name: "unavailable", preferred: 8080, fail: true, addresses: []string{"127.0.0.1:8080", "127.0.0.1:0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var addresses []string
			listener := &portListener{port: tc.want}
			port, err := freePort(tc.preferred, func(network, address string) (net.Listener, error) {
				if network != "tcp" {
					t.Errorf("network %q", network)
				}
				addresses = append(addresses, address)
				if tc.fail || tc.busy && len(addresses) == 1 {
					return nil, errors.New("busy")
				}
				return listener, nil
			})
			if (err != nil) != tc.fail || port != tc.want {
				t.Fatalf("port=%d err=%v", port, err)
			}
			if !reflect.DeepEqual(addresses, tc.addresses) {
				t.Errorf("addresses=%v", addresses)
			}
			if !tc.fail && !listener.closed {
				t.Error("reservation was not released")
			}
		})
	}
	for _, invalid := range []int{-1, 65536} {
		if _, err := FreePort(invalid); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("invalid port %d: %v", invalid, err)
		}
	}
}

func TestAllocateUniquePorts(t *testing.T) {
	t.Parallel()
	ports, err := allocate(Options{Port: 8080, APIPort: 8080, LGTM: true, PortAllocator: func(preferred int) (int, error) {
		if preferred == 0 {
			return 32000, nil
		}
		return preferred, nil
	}}, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ports.API.Port != 8080 || ports.Web.Port != 32000 || ports.Mailpit.Port != 8025 || ports.Grafana.Port != 3000 {
		t.Fatalf("ports: %+v", ports)
	}
}

func TestNoInfraOmitsMailpit(t *testing.T) {
	t.Parallel()
	ports, err := allocate(Options{NoInfra: true, PortAllocator: func(port int) (int, error) {
		return port, nil
	}}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ports.Mailpit.Port != 0 {
		t.Fatalf("Mailpit port = %d, want 0", ports.Mailpit.Port)
	}

	var out bytes.Buffer
	printURLs(&out, ports, false, false)
	if strings.Contains(out.String(), "Mailpit") {
		t.Fatalf("printed unavailable Mailpit URL: %s", out.String())
	}
}

func TestAllocateReusesPortsOfRunningServices(t *testing.T) {
	t.Parallel()
	// A previous spinup dev left Mailpit running on 8025 and LGTM on 3001. The probe
	// reports both as busy; allocate must keep them instead of moving the services.
	probe := func(preferred int) (int, error) {
		if preferred == 8025 || preferred == 3001 || preferred == 3000 {
			return 41000 + preferred, nil
		}
		return preferred, nil
	}
	running := map[string]int{"mailpit:8025": 8025, "otel-lgtm:3000": 3001}
	ports, err := allocate(Options{LGTM: true, PortAllocator: probe}, true, running, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ports.Mailpit.Port != 8025 || ports.Grafana.Port != 3001 || ports.API.Port != 8080 || ports.Web.Port != 5173 {
		t.Fatalf("ports: %+v", ports)
	}
}

func TestParsePublishedPorts(t *testing.T) {
	t.Parallel()
	lines := `{"Service":"postgres","State":"running","Publishers":[{"URL":"127.0.0.1","TargetPort":5432,"PublishedPort":5432,"Protocol":"tcp"}]}
{"Service":"mailpit","State":"running","Publishers":[{"URL":"127.0.0.1","TargetPort":1025,"PublishedPort":1025,"Protocol":"tcp"},{"URL":"127.0.0.1","TargetPort":8025,"PublishedPort":59970,"Protocol":"tcp"}]}
{"Service":"otel-lgtm","State":"exited","Publishers":[{"TargetPort":3000,"PublishedPort":3000}]}`
	array := `[{"Service":"mailpit","State":"running","Publishers":[{"TargetPort":8025,"PublishedPort":8025}]}]`
	tests := []struct {
		name string
		data string
		want map[string]int
	}{
		{"lines", lines, map[string]int{"postgres:5432": 5432, "mailpit:1025": 1025, "mailpit:8025": 59970}},
		{"array", array, map[string]int{"mailpit:8025": 8025}},
		{"empty", "", map[string]int{}},
		{"garbage", "not json", map[string]int{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parsePublishedPorts([]byte(tc.data)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
