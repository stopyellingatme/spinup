package devloop

import (
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
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if ports.API.Port != 8080 || ports.Web.Port != 32000 || ports.Mailpit.Port != 8025 || ports.Grafana.Port != 3000 {
		t.Fatalf("ports: %+v", ports)
	}
}
