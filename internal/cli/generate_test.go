package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/ui"
)

type fakeGeneratePrompter struct {
	kind, line string
	err        error
	called     bool
}

func (p *fakeGeneratePrompter) Prompt(_ context.Context, kinds []gen.Generator) (string, string, error) {
	p.called = true
	return p.kind, p.line, p.err
}

func TestGenerateCLI(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"g"}, {"generate"}, {"g", "--help"}, {"generate", "--help"}, {"g", "nope"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			code, out, stderr := run(t, args...)
			help := args[len(args)-1] == "--help"
			if help {
				if code != 0 || !strings.Contains(out, "generate") {
					t.Fatalf("code=%d out=%s err=%s", code, out, stderr)
				}
			} else if code == 0 || !strings.Contains(stderr, "Try:") {
				t.Fatalf("code=%d out=%s err=%s", code, out, stderr)
			}
			if len(args) == 1 && !strings.Contains(out, "Available generators:") {
				t.Fatalf("kinds not listed: %s", out)
			}
		})
	}
}

func TestGeneratePicker(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, kind, line string
		interactive      bool
		promptErr        error
		wantErr, help    bool
	}{
		{name: "help", kind: "example", line: "--help", interactive: true, help: true},
		{name: "selected", kind: "example", line: "todo title:string --tag selected", interactive: true},
		{name: "alias", kind: "e", line: "todo --tag alias", interactive: true},
		{name: "no-tty", wantErr: true}, {name: "cancel", interactive: true, promptErr: errors.New("canceled"), wantErr: true},
		{name: "bad-kind", kind: "bad", interactive: true, wantErr: true}, {name: "bad-flag", kind: "example", line: "--invalid", interactive: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "spinup.toml"), nil, 0644); err != nil {
				t.Fatal(err)
			}
			if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			app := &App{Flags: &GlobalFlags{Dir: root}, UI: ui.New(nil, &out, &out, ui.Options{})}
			called := false
			g := gen.Generator{Name: "example", Aliases: []string{"e"}, Short: "Example generator", Usage: "<name> [fields...]", Flags: func(fs *pflag.FlagSet) { fs.String("tag", "", "tag") }, Run: func(ctx context.Context, in gen.Input) (*gen.Output, error) {
				called = true
				if ctx == nil {
					t.Fatal("picker lost context")
				}
				if len(in.Args) == 0 || in.Args[0] != "todo" {
					t.Fatalf("args=%v", in.Args)
				}
				tag, _ := in.Flags.GetString("tag")
				if tag == "" {
					t.Fatal("picker lost flags")
				}
				return &gen.Output{}, nil
			}}
			p := &fakeGeneratePrompter{kind: tc.kind, line: tc.line, err: tc.promptErr}
			cmd := generateCommand(app, []gen.Generator{g}, p, func() bool { return tc.interactive })
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(nil)
			err := cmd.ExecuteContext(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v output=%s", err, out.String())
			}
			if !tc.interactive && p.called {
				t.Fatal("prompted without terminal")
			}
			if tc.help && (called || !strings.Contains(out.String(), "Example generator")) {
				t.Fatalf("picker help output=%s called=%v", out.String(), called)
			}
			if !tc.wantErr && !tc.help && !called {
				t.Fatal("did not run selected generator")
			}
		})
	}
}

func TestGenerateSubcommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := &App{Flags: &GlobalFlags{Dir: root}, UI: ui.New(nil, &out, &out, ui.Options{})}
	called := false
	g := gen.Generator{Name: "example", Aliases: []string{"e"}, Usage: "<name>", Flags: func(fs *pflag.FlagSet) { fs.Bool("custom", false, "") }, Run: func(_ context.Context, in gen.Input) (*gen.Output, error) {
		called = true
		if !reflect.DeepEqual(in.Args, []string{"todo"}) {
			t.Fatalf("args=%v", in.Args)
		}
		custom, _ := in.Flags.GetBool("custom")
		if !custom {
			t.Fatal("missing custom flag")
		}
		return &gen.Output{}, nil
	}}
	c := generateCommand(app, []gen.Generator{g}, nil, func() bool { return false })
	wrapper := &cobra.Command{Use: "spinup"}
	wrapper.PersistentFlags().BoolVar(&app.Flags.DryRun, "dry-run", false, "")
	wrapper.AddCommand(c)
	wrapper.SetOut(&out)
	wrapper.SetArgs([]string{"g", "e", "todo", "--custom", "--diff", "--force", "--dry-run"})
	if err := wrapper.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("registered subcommand did not run")
	}
}
