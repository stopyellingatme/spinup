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
