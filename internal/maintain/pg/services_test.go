package pg

import (
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/deploy"
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

// TestStopColorsScriptStopsWholeProjects checks that the stop selects the
// containers by color project only. A worker or a sidecar of the deployed
// release stops even when the local spinup.toml does not declare it.
func TestStopColorsScriptStopsWholeProjects(t *testing.T) {
	t.Parallel()
	script := stopColorsScript("demo")
	want := ""
	for _, color := range []string{"blue", "green"} {
		want += "ids=$(" + shell("docker", "ps", "-q", "--filter", "label=com.docker.compose.project=demo-"+color) + ")\n"
		want += "if [ -n \"$ids\" ]; then docker stop $ids; fi\n"
	}
	if script != want {
		t.Fatalf("script =\n%s\nwant\n%s", script, want)
	}
	if strings.Contains(script, "com.docker.compose.service=") {
		t.Fatalf("the stop names services: %s", script)
	}
}

// enableExtraServices gives the deployed release a sidecar and the local config
// the jobs module. The flows must not depend on either for their service lists.
func enableExtraServices(t *testing.T, u *upgrade) {
	t.Helper()
	u.cfg.Modules = append(u.cfg.Modules, "jobs")
	// The release remains authoritative even if the local declaration differs.
	u.rel.Services = []deploy.ReleaseService{{Name: "ocr", Port: 8080, Health: "/healthz"}}
}

// expectedAppStart is the start of the live color: the whole project, with no
// service names, and the Compose files of the release.
func expectedAppStart(u *upgrade, extras bool) []string {
	files := []string{u.dir + "/compose.yml"}
	if extras {
		files = append(files, u.dir+"/compose.services.yml")
	}
	return deploy.ComposeArgv("demo-blue", u.dir, files, "up", "-d", "--no-deps", "--wait")
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
			if !strings.Contains(string(upload.Data), stopColorsScript("demo")) {
				t.Fatalf("recovery lacks the stop of both color projects: %s", upload.Data)
			}
			return
		}
	}
	t.Fatal("missing rollback script")
}
