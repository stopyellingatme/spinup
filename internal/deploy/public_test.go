package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/services"
)

func publicService(name, prefix, auth string, port, devPort int) services.Service {
	return services.Service{Name: name, Public: prefix, Auth: auth, Port: port, DevPort: devPort, Health: "/healthz"}
}

func TestPublicRoutesGolden(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		list []services.Service
	}{
		{"none", []services.Service{publicService("ocr", "/svc/ocr", services.AuthNone, 8081, 8101)}},
		{"session", []services.Service{publicService("ocr", "/svc/ocr", services.AuthSession, 8081, 8101)}},
		{"mixed", []services.Service{
			publicService("search", "/search", services.AuthSession, 9090, 8103),
			publicService("private", "", "", 8082, 8102),
			publicService("ocr", "/svc/ocr", "", 8081, 8101),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before := slices.Clone(tt.list)
			got, err := PublicRoutes(tt.list)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", "public", tt.name+".caddy"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("public routes differ\ngot:\n%s\nwant:\n%s", got, want)
			}
			if !reflect.DeepEqual(tt.list, before) {
				t.Fatal("PublicRoutes changed the input")
			}
			slices.Reverse(before)
			reversed, err := PublicRoutes(before)
			if err != nil || !bytes.Equal(got, reversed) {
				t.Fatalf("declaration order changed the output: %s, %v", reversed, err)
			}
		})
	}
}

func TestPublicRoutesAbsent(t *testing.T) {
	t.Parallel()
	for _, list := range [][]services.Service{
		nil,
		{},
		{publicService("ocr", "", "", 8081, 8101)},
	} {
		got, err := PublicRoutes(list)
		if got != nil || err != nil {
			t.Errorf("PublicRoutes(%+v) = %q, %v, want nil, nil", list, got, err)
		}
	}
}

func TestPublicRoutesInvalid(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		change func(*services.Service)
	}{
		{"name", func(s *services.Service) { s.Name = "ocr\nrespond 200" }},
		{"port", func(s *services.Service) { s.Port = 65536 }},
		{"relative", func(s *services.Service) { s.Public = "svc/ocr" }},
		{"trailing slash", func(s *services.Service) { s.Public = "/svc/ocr/" }},
		{"root", func(s *services.Service) { s.Public = "/" }},
		{"injection", func(s *services.Service) { s.Public = "/svc/ocr\nrespond 200" }},
		{"reserved", func(s *services.Service) { s.Public = "/api/ocr" }},
		{"auth", func(s *services.Service) { s.Auth = "unknown" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := publicService("ocr", "/svc/ocr", "", 8081, 8101)
			tt.change(&s)
			got, err := PublicRoutes([]services.Service{s})
			if got != nil || err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("PublicRoutes = %q, %v, want nil and an error with Try:", got, err)
			}
		})
	}
	for _, prefix := range []string{"/svc/ocr", "/svc/ocr/nested", "/svc"} {
		got, err := PublicRoutes([]services.Service{
			publicService("ocr", "/svc/ocr", "", 8081, 8101),
			publicService("search", prefix, "", 8082, 8102),
		})
		if got != nil || err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Errorf("overlapping prefix %q: got %q, %v", prefix, got, err)
		}
	}
}

func TestPublicRoutesCaddy(t *testing.T) {
	t.Parallel()
	if _, err := runexec.LookPath("caddy"); err != nil {
		t.Skip("caddy is not installed")
	}
	for _, tt := range []struct {
		name     string
		upstream []byte
	}{
		{"blue", UpstreamFile(Blue)},
		{"green", UpstreamFile(Green)},
		{"placeholder", PlaceholderUpstream},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			routes, err := PublicRoutes([]services.Service{
				publicService("ocr", "/svc/ocr", services.AuthNone, 8081, 8101),
				publicService("search", "/search", services.AuthSession, 9090, 8102),
				publicService("report", "/report", services.AuthSession, 9091, 8103),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(PublicRoutesFile)), 0755); err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{
				PublicRoutesFile: routes,
				"upstream.caddy": tt.upstream,
				"Caddyfile": []byte(`:8080 {
	import Caddyfile.d/*.caddy
	handle /api/* {
		import upstream.caddy api 8080
	}
	handle {
		import upstream.caddy web 3000
	}
}
`),
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			result, err := (runexec.OSRunner{}).Run(t.Context(), runexec.Cmd{
				Name: "caddy", Args: []string{"adapt", "--config", "Caddyfile", "--adapter", "caddyfile"}, Dir: dir,
			})
			if err != nil {
				t.Fatalf("Caddy cannot adapt the routes: %v\n%s", err, result.Stderr)
			}
		})
	}
}
