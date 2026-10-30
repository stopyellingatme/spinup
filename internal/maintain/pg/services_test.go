package pg

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestColorUsesReleaseFiles(t *testing.T) {
	t.Parallel()
	for _, sidecar := range []bool{false, true} {
		name := "legacy"
		if sidecar {
			name = "sidecar"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, _, _ := fixture(t, true)
			files := []string{u.dir + "/compose.yml"}
			if sidecar {
				u.rel.Services = []deploy.ReleaseService{{Name: "ocr", Port: 8080, Health: "/healthz"}}
				files = append(files, u.dir+"/compose.services.yml")
			}
			want := deploy.ComposeArgv("demo-blue", u.dir, files, "stop", "api")
			if got := u.color("stop", "api"); !slices.Equal(got, want) {
				t.Fatalf("argv = %v, want %v", got, want)
			}
		})
	}
}

func enableExtraServices(t *testing.T, u *upgrade) {
	t.Helper()
	u.cfg.Modules = append(u.cfg.Modules, "jobs")
	// The release remains authoritative even if the local declaration differs.
	u.rel.Services = []deploy.ReleaseService{{Name: "ocr", Port: 8080, Health: "/healthz"}}
}

func expectedAppStart(u *upgrade, extras bool) []string {
	files := []string{u.dir + "/compose.yml"}
	args := []string{"up", "-d", "--no-deps", "--wait", "api"}
	if extras {
		files = append(files, u.dir+"/compose.services.yml")
		args = append(args, "worker", "ocr")
	}
	return deploy.ComposeArgv("demo-blue", u.dir, files, args...)
}

func assertExtraServiceCommands(t *testing.T, u *upgrade, f *remote.Fake) {
	t.Helper()
	want := expectedAppStart(u, true)
	found := false
	for _, call := range f.Calls() {
		if slices.Equal(call.Argv, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing exact start argv: %v", want)
	}
	for _, upload := range f.Uploads() {
		if strings.HasSuffix(upload.Path, "/rollback.sh") {
			if !strings.Contains(string(upload.Data), shell(want...)+"\n") {
				t.Fatalf("recovery lacks exact start argv: %s", upload.Data)
			}
			for _, color := range []string{"blue", "green"} {
				for _, service := range []string{"api", "worker", "ocr"} {
					query := shell("docker", "ps", "-q", "--filter", "label=com.docker.compose.project=demo-"+color, "--filter", "label=com.docker.compose.service="+service)
					if !strings.Contains(string(upload.Data), query) {
						t.Fatalf("recovery lacks %s", query)
					}
				}
			}
			return
		}
	}
	t.Fatal("missing rollback script")
}

func TestAppServicesKeepsWebSmokeSelection(t *testing.T) {
	t.Parallel()
	for _, web := range []bool{false, true} {
		name := "api only"
		if web {
			name = "web"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, _, _ := fixture(t, true)
			enableExtraServices(t, u)
			lock := manifest.New()
			lock.Packs = []string{"base", "api"}
			if web {
				lock.Packs = append(lock.Packs, "web")
			}
			if err := lock.Save(filepath.Join(u.root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			want := []string{"api"}
			if web {
				want = append(want, "web")
			}
			if got := u.services(); !slices.Equal(got, want) {
				t.Fatalf("smoke services = %v", got)
			}
			want = append(want, "worker", "ocr")
			if got := u.appServices(); !slices.Equal(got, want) {
				t.Fatalf("app services = %v, want %v", got, want)
			}
		})
	}
}
