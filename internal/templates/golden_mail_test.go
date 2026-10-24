package templates_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	spexec "github.com/stopyellingatme/spinup/internal/exec"

	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenMail(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "mail")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "web", "auth", "mail", "jobs"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "auth,mail,jobs"})
		selected := fstest.MapFS{}
		err := fs.WalkDir(all, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			if !strings.HasPrefix(path, "api/internal/mail/") && path != "api/db/migrations/00003_password_resets.sql" && !strings.HasPrefix(path, "web/src/routes/reset/") && path != "api/cmd/api/main.go" && path != "api/cmd/api/reset.go" && path != "api/cmd/worker/main.go" {
				return nil
			}
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: 0o644}
			return nil
		})
		return selected, err
	}
	files, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "mail", files)
	testutil.Deterministic(t, render)
}

func TestMailModuleCombinations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		modules       string
		packs         []string
		worker, reset bool
	}{
		{"mail", []string{"base", "api", "mail"}, false, false},
		{"auth,mail", []string{"base", "api", "web", "auth", "mail"}, false, true},
		{"mail,jobs", []string{"base", "api", "mail", "jobs"}, true, false},
	} {
		t.Run(tc.modules, func(t *testing.T) {
			t.Parallel()
			all := testutil.RenderPack(t, tc.packs, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": tc.modules})
			_, err := fs.Stat(all, "api/internal/mail/worker.go")
			if (err == nil) != tc.worker {
				t.Fatalf("mail worker: %v", err)
			}
			_, err = fs.Stat(all, "api/cmd/api/reset.go")
			if (err == nil) != tc.reset {
				t.Fatalf("reset routes: %v", err)
			}
			main, err := fs.ReadFile(all, "api/cmd/api/main.go")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(main), "mailapp.New(pool)") {
				t.Fatal("missing mail initialization")
			}
		})
	}
}

// TestMailLoginTemplate checks the prepared source before the shared metadata selects it.
func TestMailLoginTemplate(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "without mail", true: "with mail"}[enabled], func(t *testing.T) {
			t.Parallel()
			base, err := templates.Load(templates.Embedded(), "base")
			if err != nil {
				t.Fatal(err)
			}
			base.Files = nil
			auth, err := templates.Load(templates.Embedded(), "auth")
			if err != nil {
				t.Fatal(err)
			}
			auth.Files = []templates.FileSpec{{Path: "web/src/routes/login/+page.svelte", Source: "files/web/src/routes/login/+page.svelte.tmpl", Class: "scaffold"}}
			modules := "auth"
			if enabled {
				modules += ",mail"
			}
			plan, err := render.Render(render.Input{Packs: []*templates.Pack{base, auth}, Answers: map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": modules}})
			if err != nil {
				t.Fatal(err)
			}
			file, ok := plan.Get("web/src/routes/login/+page.svelte")
			if !ok {
				t.Fatal("missing page")
			}
			if strings.Contains(string(file.Data), "Forgot password?") != enabled {
				t.Fatal("mail condition did not control the link")
			}
		})
	}
}

func TestMailStandardWorkerQueues(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "jobs only", true: "jobs and mail"}[enabled], func(t *testing.T) {
			t.Parallel()
			packs := []string{"base", "api", "jobs"}
			modules := "jobs"
			if enabled {
				packs = append(packs, "mail")
				modules += ",mail"
			}
			all := testutil.RenderPack(t, packs, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": modules})
			data, err := fs.ReadFile(all, "api/cmd/worker/main.go")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`mailapp "example.com/todo/api/internal/mail"`, `jobs.WithQueue("mail", 10)`, "mailapp.New(pool)", "mailers.Register(w)"} {
				if strings.Contains(string(data), want) != enabled {
					t.Errorf("mail enabled %t: worker wiring %q", enabled, want)
				}
			}
			if enabled && !strings.Contains(string(data), "jobs.WithQueue(jobs.DefaultQueue, jobs.DefaultQueueWorkers)") {
				t.Fatal("mail queue configuration removed the default queue")
			}
		})
	}
}

// TestMailStandardWorkerDelivery compiles the rendered worker and runs its queue regression.
// With DATABASE_URL, the regression also delivers mail through River and the log transport.
func TestMailStandardWorkerDelivery(t *testing.T) {
	t.Parallel()
	all := testutil.RenderPack(t, []string{"base", "api", "jobs", "mail"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "jobs,mail"})
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, all); err != nil {
		t.Fatal(err)
	}
	kit, err := filepath.Abs("../../kit")
	if err != nil {
		t.Fatal(err)
	}
	workspace := "go 1.26.0\nuse ./api\nreplace github.com/stopyellingatme/spinup/kit => " + strconv.Quote(kit) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.work"), []byte(workspace), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api/cmd/worker/mail_test.go"), []byte(mailWorkerRegression), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (spexec.OSRunner{}).Run(t.Context(), spexec.Cmd{
		Name: "go", Args: []string{"test", "-race", "-v", "./cmd/worker", "./internal/mail"},
		Dir: filepath.Join(dir, "api"), Timeout: 5 * time.Minute, // a -race build is slow on CI runners
		Env: []string{"GOWORK=" + filepath.Join(dir, "go.work"), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off"},
	})
	if err != nil {
		t.Fatalf("rendered worker regression: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Log("rendered worker compiled. Queue delivery requires DATABASE_URL")
	}
}

const mailWorkerRegression = `package main

import (
 "context"
 "io"
 "log/slog"
 "os"
 "slices"
 "testing"
 "time"

 mailapp "example.com/todo/api/internal/mail"
 "github.com/jackc/pgx/v5/pgxpool"
 "github.com/stopyellingatme/spinup/kit/jobs"
 kitmail "github.com/stopyellingatme/spinup/kit/mail"
 "github.com/stopyellingatme/spinup/kit/testkit"
)

type defaultArgs struct{}
func (defaultArgs) Kind() string { return "test.default" }

func mailEnvironment(t *testing.T) {
 t.Helper()
 t.Setenv("SPINUP_ENV", "production")
 for _, key := range []string{"SMTP_URL_FILE", "SMTP_PASSWORD_FILE", "SMTP_TIMEOUT_FILE", "MAIL_FROM_FILE"} {
  // Register restoration before removing the secret-file environment variable.
  t.Setenv(key, "")
  if err := os.Unsetenv(key); err != nil { t.Fatal(err) }
 }
 t.Setenv("SMTP_URL", "")
 t.Setenv("SMTP_PASSWORD", "")
 t.Setenv("SMTP_TIMEOUT", "10s")
 t.Setenv("MAIL_FROM", "app@example.com")
}

func TestStandardWorkerRegistersMail(t *testing.T) {
 mailEnvironment(t)
 // Creating a pool does not open a connection. Registration needs no database.
 pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused")
 if err != nil { t.Fatal(err) }
 defer pool.Close()
 w, err := newWorker(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
 if err != nil { t.Fatal(err) }
 if !slices.Contains(w.Kinds(), "mail.send") { t.Fatalf("mail handler is absent: %v", w.Kinds()) }
}

func TestStandardWorkerDeliversMailAndDefaultJobs(t *testing.T) {
 if os.Getenv("DATABASE_URL") == "" { t.Skip("queue delivery requires DATABASE_URL") }
 mailEnvironment(t)
 pool := testkit.DB(t)
 ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
 defer cancel()
 if err := jobs.MigrationHook(jobs.WithMigrateOutput(io.Discard)).AfterUp(ctx, testkit.URL(t, pool)); err != nil { t.Fatal(err) }
 w, err := newWorker(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
 if err != nil { t.Fatal(err) }
 ran := make(chan struct{}, 1)
 jobs.Register(w, func(context.Context, jobs.Job[defaultArgs]) error { ran <- struct{}{}; return nil })
 mailers, err := mailapp.New(pool)
 if err != nil { t.Fatal(err) }
 if err := mailers.Send(ctx, kitmail.Message{From: "app@example.com", To: "user@example.com", Subject: "Reset", Text: "Reset link"}); err != nil { t.Fatal(err) }
 client, err := jobs.New(pool)
 if err != nil { t.Fatal(err) }
 if _, err := client.Insert(ctx, defaultArgs{}); err != nil { t.Fatal(err) }
 if err := w.Start(ctx); err != nil { t.Fatal(err) }
 defer func() {
  stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
  defer cancel()
  if err := w.Stop(stop); err != nil { t.Error(err) }
 }()
 select {
 case <-ran:
 case <-ctx.Done(): t.Fatal("standard worker did not consume the default queue")
 }
 tick := time.NewTicker(10*time.Millisecond)
 defer tick.Stop()
 for {
  var state string
  if err := pool.QueryRow(ctx, "SELECT state::text FROM river_job WHERE kind = 'mail.send'").Scan(&state); err != nil { t.Fatal(err) }
  if state == "completed" { return }
  select {
  case <-tick.C:
  case <-ctx.Done(): t.Fatalf("mail delivery did not complete: %s", state)
  }
 }
}
`
