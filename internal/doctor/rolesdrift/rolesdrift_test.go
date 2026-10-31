package rolesdrift

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestCheck(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, roles, file, main, want string
		status                        doctor.Status
	}{
		{name: "current", roles: "[auth]\nroles = [\"owner\", \"staff\"]\nadmin_role = \"owner\"\ndefault_role = \"staff\"\n", file: "custom", main: completeMain("authn"), status: doctor.OK},
		{name: "missing file", roles: "", main: completeMain("authn"), status: doctor.Warn, want: "does not match"},
		{name: "drift", roles: "", file: "old", main: completeMain("authn"), status: doctor.Warn, want: "does not match"},
		{name: "missing registration", roles: "", file: "default", main: "", status: doctor.Warn, want: "does not pass"},
		{name: "missing registration call", roles: "", file: "default", main: "package main\nfunc main() { auth.WithRoles(roles.Set); app.WithCommand(\"user\", auth.UserCommand(a)) }\n", status: doctor.Warn, want: "does not pass"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write(t, root, project.ConfigFile, tt.roles)
			lock := manifest.New()
			lock.Packs = []string{"base", "api", "auth"}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			switch tt.file {
			case "custom":
				write(t, root, project.RolesFilePath, string(project.RenderRolesFile(project.AuthRoles{Names: []string{"owner", "staff"}, Admin: "owner", Default: "staff"})))
			case "default":
				write(t, root, project.RolesFilePath, string(project.RenderRolesFile((&project.Config{}).AuthRoles())))
			case "":
			default:
				write(t, root, project.RolesFilePath, tt.file)
			}
			write(t, root, "api/cmd/api/main.go", tt.main)

			got := runCheck(context.Background(), &doctor.Context{Root: root, Quick: true})
			if got.Status != tt.status || !strings.Contains(got.Message, tt.want) {
				t.Fatalf("result = %+v", got)
			}
			if got.Status == doctor.Warn && got.Try == "" {
				t.Fatal("warning has no Try line")
			}
		})
	}
}

func completeMain(authenticator string) string {
	return "package main\nfunc main() { auth.WithRoles(roles.Set); auth.WithRegistration(auth.RegistrationMode(os.Getenv(\"AUTH_REGISTRATION\"))); app.WithCommand(\"user\", auth.UserCommand(" + authenticator + ")) }\n"
}

func TestCheckSkipsOutsideAndWithoutAuth(t *testing.T) {
	t.Parallel()
	if got := runCheck(context.Background(), &doctor.Context{}); got.Status != doctor.Skip {
		t.Fatalf("outside = %+v", got)
	}
	root := t.TempDir()
	write(t, root, project.ConfigFile, "")
	lock := manifest.New()
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(context.Background(), &doctor.Context{Root: root, Quick: true}); got.Status != doctor.Skip {
		t.Fatalf("no auth = %+v", got)
	}
}

func write(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
