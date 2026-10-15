package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestMigrate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sub, env     string
		know, yes, ci, dry bool
		code               int
		wantErr            string
	}{
		{name: "default"}, {name: "down guard", sub: "down", wantErr: "Try: spinup db migrate down --i-know"},
		{name: "protected", sub: "down", env: "production", know: true, wantErr: "Try:"},
		{name: "yes alone", sub: "down", env: "production", know: true, yes: true, wantErr: "Try:"},
		{name: "CI approval", sub: "down", env: "production", know: true, yes: true, ci: true},
		{name: "dry", dry: true}, {name: "status", sub: "status"}, {name: "exit", code: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname='todo'\n[env.production]\nprotected=true\n"), 0644); err != nil {
				t.Fatal(err)
			}
			fake := &run.Fake{}
			sub := tc.sub
			if sub == "" {
				sub = "up"
			}
			if tc.wantErr == "" && !tc.dry {
				fake.Expect("go", "run", "./cmd/api", "migrate", sub).Return("out\n", "err\n", tc.code)
			}
			var out, stderr bytes.Buffer
			err := Migrate(context.Background(), Options{Dir: root, Sub: tc.sub, Env: tc.env, IKnow: tc.know, Yes: tc.yes, CI: tc.ci, DryRun: tc.dry, Out: &out, ErrOut: &stderr}, fake)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(errorText(err), tc.wantErr) {
					t.Fatalf("error=%v", err)
				}
				if len(fake.Calls()) != 0 {
					t.Fatal("ran guarded command")
				}
				return
			}
			if tc.code != 0 {
				if ui.ExitCode(err) != tc.code {
					t.Fatalf("exit=%d, err=%v", ui.ExitCode(err), err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			if tc.dry {
				if !strings.Contains(out.String(), "go run ./cmd/api migrate up") {
					t.Fatal(out.String())
				}
				return
			}
			got := fake.Calls()[0]
			if got.Dir != filepath.Join(root, "api") || len(got.Env) != 0 {
				t.Fatalf("command=%#v", got)
			}
			if out.String() != "out\n" || stderr.String() != "err\n" {
				t.Fatalf("streams=%q,%q", out.String(), stderr.String())
			}
		})
	}
}

func errorText(err error) string {
	var ue *ui.Error
	if errors.As(err, &ue) {
		return err.Error() + "\nTry: " + ue.Try
	}
	return err.Error()
}

func TestMigrateEnvironmentGuards(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, config, env, sub string
		dry, yes, ci           bool
		wantErr                string
	}{
		{name: "protected default", config: "default_env='prod'\n[env.prod]\nprotected=true\n", wantErr: "protected environment"},
		{name: "protected default dry run", config: "default_env='prod'\n[env.prod]\nprotected=true\n", dry: true, wantErr: "protected environment"},
		{name: "protected only environment", config: "[env.prod]\nprotected=true\n", dry: true, wantErr: "protected environment"},
		{name: "yes needs CI", config: "[env.prod]\nprotected=true\n", dry: true, yes: true, wantErr: "protected environment"},
		{name: "CI confirmation", config: "[env.prod]\nprotected=true\n", dry: true, yes: true, ci: true},
		{name: "unprotected default", config: "default_env='dev'\n[env.dev]\nprotected=false\n", dry: true},
		{name: "ambiguous environments", config: "[env.dev]\n[env.prod]\n", dry: true, wantErr: "could not resolve environment"},
		{name: "ambiguous up", config: "[env.dev]\n[env.prod]\n", sub: "up", dry: true, wantErr: "could not resolve environment"},
		{name: "local database", dry: true},
		{name: "unknown local environment", env: "prod", dry: true, wantErr: "could not resolve environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(tc.config+"\n[app]\nname='todo'\n"), 0644); err != nil {
				t.Fatal(err)
			}
			sub := tc.sub
			if sub == "" {
				sub = "down"
			}
			fake := &run.Fake{}
			var out bytes.Buffer
			err := Migrate(context.Background(), Options{Dir: root, Sub: sub, Env: tc.env, IKnow: true, DryRun: tc.dry, Yes: tc.yes, CI: tc.ci, Out: &out}, fake)
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(errorText(err), tc.wantErr) || !strings.Contains(errorText(err), "Try:") {
					t.Fatalf("error=%v, want %q with Try line", err, tc.wantErr)
				}
				if out.Len() != 0 {
					t.Fatalf("refused migration printed a command: %s", out.String())
				}
			case err != nil:
				t.Fatal(err)
			case !strings.Contains(out.String(), "go run ./cmd/api migrate "+sub):
				t.Fatalf("missing dry-run command: %s", out.String())
			}
			if len(fake.Calls()) != 0 {
				t.Fatal("ran guarded or dry-run command")
			}
		})
	}
}

func TestMigrateProcessErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		startErr error
		wantWhy  string
	}{
		{name: "nonzero exit", wantWhy: "go run ./cmd/api migrate up exited with status 1"},
		{name: "cannot start", startErr: &run.NotFoundError{Name: "go"}, wantWhy: (&run.NotFoundError{Name: "go"}).Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname='todo'\n"), 0644); err != nil {
				t.Fatal(err)
			}
			fake := &run.Fake{}
			expect := fake.Expect("go", "run", "./cmd/api", "migrate", "up")
			if tc.startErr != nil {
				expect.Fail(tc.startErr)
			} else {
				expect.Return("", "migration failed: unique diagnostic\n", 1)
			}
			var out, stderr bytes.Buffer
			err := Migrate(context.Background(), Options{Dir: root, Out: &out, ErrOut: &stderr}, fake)
			var ue *ui.Error
			if !errors.As(err, &ue) || ue.Why != tc.wantWhy {
				t.Fatalf("error=%v, want Why=%q", err, tc.wantWhy)
			}
			if tc.startErr == nil {
				if strings.Count(stderr.String()+errorText(err), "unique diagnostic") != 1 || strings.Count(errorText(err), "Try:") != 1 {
					t.Fatalf("duplicate output or guidance: %s\n%s", stderr.String(), errorText(err))
				}
			}
			fake.AssertDone(t)
		})
	}
}
