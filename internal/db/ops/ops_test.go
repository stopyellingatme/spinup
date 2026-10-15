package ops

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

func errorText(err error) string {
	var ue *ui.Error
	if errors.As(err, &ue) {
		return err.Error() + "\nTry: " + ue.Try
	}
	return err.Error()
}

// newProject writes a project with spinup.toml (plus config) and the given files.
func newProject(t *testing.T, config string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["spinup.toml"] = config + "\n[app]\nname='todo'\n"
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestNextMigrationName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, arg string
		files     []string
		want      string
		wantErr   string
	}{
		{name: "missing dir", arg: "init", want: "00001_init.sql"},
		{name: "empty dir", arg: "init", files: []string{}, want: "00001_init.sql"},
		{name: "next", arg: "add_widgets", files: []string{"00001_init.sql", "embed.go"}, want: "00002_add_widgets.sql"},
		{name: "highest plus one", arg: "add_tags", files: []string{"00001_init.sql", "00007_b.sql", "00003_a.sql"}, want: "00008_add_tags.sql"},
		{name: "ignores other numbering", arg: "x", files: []string{"00002_a.sql", "20260101120000_ts.sql", "007_short.sql", "00009_b.txt"}, want: "00003_x.sql"},
		{name: "digits in name", arg: "v2_users", files: []string{"00001_init.sql"}, want: "00002_v2_users.sql"},
		{name: "exists", arg: "init", files: []string{"00001_init.sql"}, wantErr: "already has the name init"},
		{name: "exists other numbering", arg: "ts", files: []string{"20260101120000_ts.sql"}, wantErr: "already has the name ts"},
		{name: "camel case", arg: "addWidgets", wantErr: "not snake case"},
		{name: "dash", arg: "add-widgets", wantErr: "not snake case"},
		{name: "leading digit", arg: "2fa", wantErr: "not snake case"},
		{name: "double underscore", arg: "a__b", wantErr: "not snake case"},
		{name: "trailing underscore", arg: "a_", wantErr: "not snake case"},
		{name: "empty", arg: "", wantErr: "not snake case"},
		{name: "path", arg: "../x", wantErr: "not snake case"},
		{name: "full", arg: "x", files: []string{"99999_last.sql"}, wantErr: "reached 99999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "migrations")
			if tc.files != nil {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				for _, f := range tc.files {
					if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := NextMigrationName(dir, tc.arg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(errorText(err), tc.wantErr) || !strings.Contains(errorText(err), "Try: ") {
					t.Fatalf("got %q, error=%v, want %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	root := newProject(t, "", map[string]string{"api/db/migrations/00001_init.sql": ""})
	var out bytes.Buffer
	if err := New(Options{Dir: root, DryRun: true, Out: &out}, "add_widgets"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Would create api/db/migrations/00002_add_widgets.sql\n" {
		t.Fatalf("dry run output %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "api/db/migrations/00002_add_widgets.sql")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote the file")
	}
	for _, want := range []string{"00002_add_widgets.sql", "00003_add_tags.sql"} {
		out.Reset()
		name := strings.TrimSuffix(want[6:], ".sql")
		if err := New(Options{Dir: filepath.Join(root, "api"), Out: &out}, name); err != nil {
			t.Fatal(err)
		}
		if out.String() != "Created api/db/migrations/"+want+"\n" {
			t.Fatalf("output %q", out.String())
		}
		data, err := os.ReadFile(filepath.Join(root, "api/db/migrations", want))
		if err != nil || string(data) != migrationSkeleton {
			t.Fatalf("file %q, %v", data, err)
		}
	}
	if err := New(Options{Dir: root, Out: &out}, "add_widgets"); err == nil {
		t.Fatal("duplicate name accepted")
	}
}

func TestMigrationCommands(t *testing.T) {
	t.Parallel()
	const prot = "[env.production]\nprotected=true\n"
	files := func() map[string]string {
		return map[string]string{"api/db/migrations/00001_init.sql": "", "api/db/migrations/00002_widgets.sql": ""}
	}
	for _, tc := range []struct {
		name, config, env, version string
		know, yes, ci, dry         bool
		run                        func(context.Context, Options, run.Runner) error
		expect                     func(f *run.Fake)
		wantOut, wantErr           string
	}{
		{name: "status dry", run: Status, dry: true, wantOut: "go run ./cmd/api migrate status\n"},
		{name: "status", run: Status, expect: func(f *run.Fake) {
			f.Expect("go", "run", "./cmd/api", "migrate", "status").Return("applied  00001_init.sql\n", "", 0)
		}, wantOut: "applied  00001_init.sql\n"},
		{name: "redo guard", run: Redo, wantErr: "Try: spinup db redo --i-know"},
		{name: "redo dry", run: Redo, know: true, dry: true, wantOut: "go run ./cmd/api migrate redo\n"},
		{name: "redo protected", run: Redo, know: true, config: prot, wantErr: "protected environment"},
		{name: "redo protected CI", run: Redo, know: true, yes: true, ci: true, config: prot, dry: true, wantOut: "go run ./cmd/api migrate redo\n"},
		{name: "redo exit", run: Redo, know: true, expect: func(f *run.Fake) { f.Expect("go", "run", "./cmd/api", "migrate", "redo").Return("", "boom\n", 3) }, wantErr: "exited with status 3"},
		{name: "to newest needs no guard", version: "2", dry: true, wantOut: "go run ./cmd/api migrate to 2\n"},
		{name: "to beyond newest", version: "5", dry: true, wantOut: "go run ./cmd/api migrate to 5\n"},
		{name: "to down dry guard", version: "1", dry: true, wantErr: "Try: spinup db to 1 --i-know"},
		{name: "to down dry", version: "1", dry: true, know: true, wantOut: "go run ./cmd/api migrate to 1\n"},
		{name: "to bad version", version: "x", wantErr: "is not a migration version"},
		{name: "to down protected", version: "0", know: true, config: prot, dry: true, wantErr: "protected environment"},
		{name: "to up ambiguous env", version: "2", config: "[env.a]\n[env.b]\n", dry: true, wantErr: "could not resolve environment"},
		{
			name: "to up after version query", version: "1",
			expect: func(f *run.Fake) {
				f.Expect("go", "run", "./cmd/api", "migrate", "version").Return("0\n", "", 0)
				f.Expect("go", "run", "./cmd/api", "migrate", "to", "1").Return("applied  00001_init.sql\n", "", 0)
			},
			wantOut: "applied  00001_init.sql\n",
		},
		{
			name: "to down after version query", version: "1",
			expect:  func(f *run.Fake) { f.Expect("go", "run", "./cmd/api", "migrate", "version").Return("2\n", "", 0) },
			wantErr: "Try: spinup db to 1 --i-know",
		},
		{
			name: "to old kit", version: "1",
			expect: func(f *run.Fake) {
				f.Expect("go", "run", "./cmd/api", "migrate", "version").Return("migrate: unknown\n", "", 0)
			},
			wantErr: "not a version number",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := newProject(t, tc.config, files())
			fake := &run.Fake{}
			if tc.expect != nil {
				tc.expect(fake)
			}
			fn := tc.run
			if fn == nil {
				fn = func(ctx context.Context, o Options, r run.Runner) error { return To(ctx, o, r, tc.version) }
			}
			var out, stderr bytes.Buffer
			err := fn(context.Background(), Options{Dir: root, Env: tc.env, IKnow: tc.know, Yes: tc.yes, CI: tc.ci, DryRun: tc.dry, Out: &out, ErrOut: &stderr}, fake)
			fake.AssertDone(t)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(errorText(err), tc.wantErr) || !strings.Contains(errorText(err), "Try:") {
					t.Fatalf("error=%v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(errorText(err))
			}
			if out.String() != tc.wantOut {
				t.Fatalf("out=%q, want %q", out.String(), tc.wantOut)
			}
			for _, c := range fake.Calls() {
				if c.Dir != filepath.Join(root, "api") {
					t.Fatalf("command dir %q", c.Dir)
				}
			}
		})
	}
}

const compose = "docker compose -f deploy/compose.dev.yml exec"

func TestDevCommandsDryRun(t *testing.T) {
	t.Parallel()
	const prot = "[env.production]\nprotected=true\n[env.dev]\nprotected=true\n"
	for _, tc := range []struct {
		name, config, env string
		yes, ci           bool
		run               func(context.Context, Options, run.Runner) error
		seeds             bool
		wantOut, wantErr  string
	}{
		{name: "psql", run: Psql, wantOut: compose + " postgres psql -U app -d app\n"},
		{name: "psql remote env", run: Psql, env: "production", config: prot, wantErr: "works only on the local development database"},
		{name: "dump", run: func(ctx context.Context, o Options, r run.Runner) error { return Dump(ctx, o, r, "") },
			wantOut: compose + " -T postgres pg_dump -U app -d app -Fc > .spinup/dumps/app.dump\n"},
		{name: "dump file", run: func(ctx context.Context, o Options, r run.Runner) error { return Dump(ctx, o, r, "my dump.bin") },
			wantOut: compose + " -T postgres pg_dump -U app -d app -Fc > 'my dump.bin'\n"},
		{name: "seed none", run: Seed, wantOut: "No seed files in api/db/seeds.\nTry: add a file such as api/db/seeds/01_users.sql with INSERT statements\n"},
		{name: "seed", run: Seed, seeds: true, wantOut: compose + " -T postgres psql -U app -d app -v ON_ERROR_STOP=1 -f - < api/db/seeds/01_a.sql\n" +
			compose + " -T postgres psql -U app -d app -v ON_ERROR_STOP=1 -f - < api/db/seeds/02_b.sql\n"},
		{name: "seed protected dev", run: Seed, env: "dev", config: prot, wantErr: "protected environment"},
		{name: "reset needs yes", run: Reset, wantErr: "Try: spinup db reset --yes"},
		{name: "reset", run: Reset, yes: true, wantOut: compose + " -T postgres psql -U app -d app -v ON_ERROR_STOP=1 -c 'DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;'\ngo run ./cmd/api migrate up\n"},
		{name: "reset production", run: Reset, yes: true, env: "production", config: prot, wantErr: "works only on the local development database"},
		{name: "reset protected dev", run: Reset, yes: true, env: "dev", config: prot, wantErr: "protected environment"},
		{name: "reset protected dev CI", run: Reset, yes: true, ci: true, env: "dev", config: prot, wantOut: compose + " -T postgres psql -U app -d app -v ON_ERROR_STOP=1 -c 'DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;'\ngo run ./cmd/api migrate up\n"},
		{name: "reset undeclared local", run: Reset, yes: true, env: "local", wantOut: compose + " -T postgres psql -U app -d app -v ON_ERROR_STOP=1 -c 'DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;'\ngo run ./cmd/api migrate up\n"},
		{name: "restore", run: func(ctx context.Context, o Options, r run.Runner) error {
			return Restore(ctx, o, r, filepath.Join(o.Dir, "x.dump"))
		}, yes: true,
			wantOut: compose + " -T postgres pg_restore -U app -d app --clean --if-exists < "},
		{name: "restore needs yes", run: func(ctx context.Context, o Options, r run.Runner) error {
			return Restore(ctx, o, r, filepath.Join(o.Dir, "x.dump"))
		}, wantErr: "--yes"},
		{name: "restore missing", run: func(ctx context.Context, o Options, r run.Runner) error { return Restore(ctx, o, r, "nope.dump") }, yes: true, wantErr: "is not a dump file"},
		{name: "doctor", run: Doctor, wantOut: compose + " -T postgres psql -U app -d app -At -c 'SELECT current_setting('\"'\"'server_version_num'\"'\"')'\ngo run ./cmd/api migrate status\n"},
		{name: "sqlc none", run: Sqlc, wantOut: "No queries in api/db/queries, so sqlc has nothing to generate.\nTry: spinup g resource <name> <field:type>...\n"},
		{name: "sqlc", run: Sqlc, seeds: true, wantOut: "go tool -modfile=tools.mod sqlc generate\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"x.dump": "PGDMP"}
			if tc.seeds {
				files["api/db/seeds/02_b.sql"] = ""
				files["api/db/seeds/01_a.sql"] = ""
				files["api/db/seeds/notes.txt"] = ""
				files["api/db/queries/todos.sql"] = ""
			}
			root := newProject(t, tc.config, files)
			fake := &run.Fake{}
			var out bytes.Buffer
			err := tc.run(context.Background(), Options{Dir: root, Env: tc.env, Yes: tc.yes, CI: tc.ci, DryRun: true, Out: &out}, fake)
			if len(fake.Calls()) != 0 {
				t.Fatalf("dry run ran %v", fake.Calls())
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(errorText(err), tc.wantErr) || !strings.Contains(errorText(err), "Try:") {
					t.Fatalf("error=%v, want %q", err, tc.wantErr)
				}
				if out.Len() != 0 {
					t.Fatalf("refused command printed %q", out.String())
				}
				return
			}
			if err != nil {
				t.Fatal(errorText(err))
			}
			if !strings.HasPrefix(out.String(), tc.wantOut) {
				t.Fatalf("out=%q\nwant %q", out.String(), tc.wantOut)
			}
		})
	}
}

func TestResetPrompt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		answer string
		ok     bool
	}{{"y\n", true}, {"YES\n", true}, {"n\n", false}, {"", false}} {
		root := newProject(t, "", map[string]string{})
		var out bytes.Buffer
		err := Reset(context.Background(), Options{Dir: root, Interactive: true, DryRun: true, In: strings.NewReader(tc.answer), Out: &out}, &run.Fake{})
		if (err == nil) != tc.ok {
			t.Fatalf("answer %q: error=%v", tc.answer, err)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Fatalf("no prompt: %q", out.String())
		}
	}
}

func TestDevCommandsRun(t *testing.T) {
	t.Parallel()
	root := newProject(t, "", map[string]string{
		"api/db/seeds/01_a.sql":    "INSERT 1;",
		"api/db/queries/todos.sql": "",
		"in.dump":                  "PGDMP-in",
	})
	fake := &run.Fake{}
	fake.Expect("docker", "compose", "-f", "deploy/compose.dev.yml", "exec", "postgres", "psql").Return("", "", 0)
	fake.Expect("docker", "compose", "-f", "deploy/compose.dev.yml", "exec", "-T", "postgres", "pg_dump").Return("PGDMP-out", "", 0)
	fake.Expect("docker", "compose", "-f", "deploy/compose.dev.yml", "exec", "-T", "postgres", "pg_restore").Return("", "", 0)
	fake.Expect("docker", "compose", "-f", "deploy/compose.dev.yml", "exec", "-T", "postgres", "psql", "-U", "app", "-d", "app", "-v", "ON_ERROR_STOP=1", "-f", "-").Return("INSERT 0 1\n", "", 0)
	fake.Expect("docker", "compose", "-f", "deploy/compose.dev.yml", "exec", "-T", "postgres", "psql", "-U", "app", "-d", "app", "-v", "ON_ERROR_STOP=1", "-c").Return("", "", 0)
	fake.Expect("go", "run", "./cmd/api", "migrate", "up").Return("migrate: no pending migrations\n", "", 0)
	fake.Expect("go", "tool", "-modfile=tools.mod", "sqlc", "generate").Return("", "", 0)
	ctx := context.Background()
	var out bytes.Buffer
	o := Options{Dir: root, Yes: true, Out: &out, ErrOut: &out}
	steps := []func() error{
		func() error { return Psql(ctx, o, fake) },
		func() error { return Dump(ctx, o, fake, "") },
		func() error { return Restore(ctx, o, fake, filepath.Join(root, "in.dump")) },
		func() error { return Seed(ctx, o, fake) },
		func() error { return Reset(ctx, o, fake) },
		func() error { return Sqlc(ctx, o, fake) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, errorText(err))
		}
	}
	fake.AssertDone(t)
	calls := fake.Calls()
	if !calls[0].Interactive || calls[0].Dir != root {
		t.Fatalf("psql call %#v", calls[0])
	}
	data, err := os.ReadFile(filepath.Join(root, ".spinup/dumps/app.dump"))
	if err != nil || string(data) != "PGDMP-out" {
		t.Fatalf("dump file %q, %v", data, err)
	}
	if calls[2].Stdin == nil || calls[3].Stdin == nil {
		t.Fatal("restore or seed has no stdin")
	}
	if calls[6].Dir != filepath.Join(root, "api") {
		t.Fatalf("sqlc dir %q", calls[6].Dir)
	}
	for _, want := range []string{"Wrote .spinup/dumps/app.dump\n", "Restored ", "seed  api/db/seeds/01_a.sql\nINSERT 0 1\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q lacks %q", out.String(), want)
		}
	}
}

func TestDockerMissing(t *testing.T) {
	t.Parallel()
	root := newProject(t, "", map[string]string{})
	fake := &run.Fake{}
	fake.Expect("docker").Fail(&run.NotFoundError{Name: "docker"})
	err := Psql(context.Background(), Options{Dir: root}, fake)
	if err == nil || !strings.Contains(errorText(err), "docker command was not found") || !strings.Contains(errorText(err), "Try: install Docker") {
		t.Fatalf("error=%v", err)
	}
}

func TestDumpFailureKeepsOldFile(t *testing.T) {
	t.Parallel()
	root := newProject(t, "", map[string]string{".spinup/dumps/app.dump": "old"})
	fake := &run.Fake{}
	fake.Expect("docker").Return("partial", "pg_dump: error\n", 1)
	var out bytes.Buffer
	err := Dump(context.Background(), Options{Dir: root, Out: &out, ErrOut: &out}, fake, "")
	if err == nil || !strings.Contains(errorText(err), "Try: ") {
		t.Fatalf("error=%v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".spinup/dumps/app.dump"))
	if string(data) != "old" {
		t.Fatalf("dump file %q", data)
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".spinup/dumps"))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func TestCheckServerVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		num        string
		major      int
		warn, fail bool
		bad        bool
	}{
		{num: "130012\n", major: 13, fail: true},
		{num: "140015", major: 14, warn: true},
		{num: "150010", major: 15},
		{num: "180001\n", major: 18},
		{num: "190000", major: 19, fail: true},
		{num: "", bad: true},
		{num: "abc", bad: true},
		{num: "96", bad: true},
	} {
		got, err := CheckServerVersion(tc.num)
		if tc.bad {
			if err == nil {
				t.Errorf("%q: no error", tc.num)
			}
			continue
		}
		if err != nil || got.Major != tc.major || (got.Warn != "") != tc.warn || (got.Fail != "") != tc.fail {
			t.Errorf("%q: %+v, %v", tc.num, got, err)
		}
	}
}

func TestDoctor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, version, status string
		wantErr               bool
		wantOut, wantStderr   string
	}{
		{name: "ok", version: "170006\n", status: "applied  00001_init.sql\n", wantOut: "ok  PostgreSQL 17\nok  no pending migrations\n"},
		{name: "eol warning and pending", version: "140015\n", status: "applied  00001_init.sql\npending  00002_a.sql\n",
			wantOut: "ok  PostgreSQL 14\n", wantStderr: "Warning: PostgreSQL 14 reaches its end of life on 2026-11-12."},
		{name: "beta", version: "190000\n", status: "", wantErr: true, wantOut: "ok  no pending migrations\n"},
		{name: "old", version: "130000\n", status: "", wantErr: true, wantOut: "ok  no pending migrations\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := newProject(t, "", map[string]string{})
			fake := &run.Fake{}
			fake.Expect("docker").Return(tc.version, "", 0)
			fake.Expect("go", "run", "./cmd/api", "migrate", "status").Return(tc.status, "", 0)
			var out, stderr bytes.Buffer
			err := Doctor(context.Background(), Options{Dir: root, Out: &out, ErrOut: &stderr}, fake)
			fake.AssertDone(t)
			if (err != nil) != tc.wantErr || (err != nil && !strings.Contains(errorText(err), "Try: ")) {
				t.Fatalf("error=%v", err)
			}
			if out.String() != tc.wantOut {
				t.Fatalf("out=%q, want %q", out.String(), tc.wantOut)
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr=%q", stderr.String())
			}
			if strings.Contains(tc.status, "pending") && !strings.Contains(stderr.String(), "1 pending migrations: 00002_a.sql") {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
}
