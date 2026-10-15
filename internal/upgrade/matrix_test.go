package upgrade_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/upgrade"
)

// TestMatrix upgrades every frozen corpus through real Git merges and recovery.
func TestMatrix(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPINUP_KIT_REPLACE", filepath.Join(repo, "kit"))
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOWORK", "off")
	source := matrixSource(t, repo)
	corpus := filepath.Join(repo, "testdata", "corpus")
	entries, err := filepath.Glob(filepath.Join(corpus, "*", "*", "expect.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no upgrade corpora found")
	}
	for _, entry := range entries {
		name, err := filepath.Rel(corpus, filepath.Dir(entry))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			ctx := t.Context()
			var want struct {
				Conflicts  []string `toml:"conflicts"`
				Orphans    []string `toml:"orphans"`
				Tombstones []string `toml:"tombstones"`
			}
			if err := toml.Unmarshal(matrixRead(t, entry), &want); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			root, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			fixture := filepath.Dir(entry)
			if err := os.CopyFS(root, os.DirFS(filepath.Join(fixture, "tree"))); err != nil {
				t.Fatal(err)
			}
			old, err := manifest.Load(filepath.Join(root, "spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if old.TemplateVersion != filepath.Base(filepath.Dir(fixture)) {
				t.Fatal("corpus directory and lock version differ")
			}
			matrixCommand(t, ctx, root, "git", "init", "-b", "main")
			matrixCommand(t, ctx, root, "git", "add", "-A")
			matrixCommand(t, ctx, root, "git", "commit", "-m", "frozen generated app")
			matrixOverlay(t, filepath.Join(fixture, "edits"), root)
			matrixCommand(t, ctx, root, "git", "add", "-A")
			matrixCommand(t, ctx, root, "git", "commit", "-m", "user edits")
			originalLock := matrixRead(t, filepath.Join(root, "spinup.lock"))
			var output bytes.Buffer
			runner := upgrade.Runner{Root: root, Source: source, Exec: matrixExec{}, Merger: merge.NewGit(exec.OSRunner{}), Out: &output, CLIVersion: "dev"}
			options := upgrade.Options{To: "0.0.1", Yes: true, NoVerify: true, Offline: true}
			runErr := runner.Run(ctx, options)
			if runErr != nil && ui.ExitCode(runErr) != ui.ExitConflicts {
				t.Fatalf("upgrade: %v\n%s", runErr, &output)
			}
			work := filepath.Join(root, ".spinup", "upgrade", options.To)
			// Inspect written markers independently of the planner's conflict classification.
			actual := matrixConflicts(t, work)
			matrixPaths(t, "conflicts", actual, want.Conflicts)
			if len(want.Conflicts) > 0 {
				if ui.ExitCode(runErr) != ui.ExitConflicts {
					t.Fatalf("want exit 3, got %v\n%s", runErr, &output)
				}
				for _, path := range actual {
					dest := filepath.Join(work, filepath.FromSlash(path))
					matrixWrite(t, dest, matrixResolve(t, matrixRead(t, dest)), 0o644)
				}
				if err := runner.Continue(ctx, options); err != nil {
					t.Fatalf("continue: %v\n%s", err, &output)
				}
			} else if runErr != nil {
				t.Fatalf("upgrade: %v\n%s", runErr, &output)
			}
			final, err := manifest.Load(filepath.Join(work, "spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if final.Schema != manifest.SchemaVersion || final.TemplateVersion != options.To || !slices.Equal(final.Packs, old.Packs) {
				t.Fatalf("unexpected final lock: schema=%d version=%s packs=%v", final.Schema, final.TemplateVersion, final.Packs)
			}
			matrixPaths(t, "orphans", final.Orphans, want.Orphans)
			matrixPaths(t, "tombstones", final.Tombstones, want.Tombstones)
			for _, path := range want.Tombstones {
				if _, err := os.Stat(filepath.Join(work, filepath.FromSlash(path))); !os.IsNotExist(err) {
					t.Fatalf("tombstone %s exists or cannot be inspected: %v", path, err)
				}
			}
			target, err := source.Load(ctx, options.To)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := upgrade.Render(target, old.Packs, old.Answers)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range rendered.Sorted().Files {
				if slices.Contains(want.Tombstones, file.Path) {
					continue
				}
				locked, exists := final.Files[file.Path]
				if !exists {
					t.Errorf("final lock omits %s", file.Path)
					continue
				}
				if locked.Class != manifest.Class(file.Class) {
					t.Errorf("%s: class changed to %s", file.Path, locked.Class)
				}
				if locked.Class != manifest.Seed && locked.Hash != manifest.Hash(file.Data) {
					t.Errorf("%s: lock does not record target template hash", file.Path)
				}
				path := filepath.Join(work, filepath.FromSlash(file.Path))
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != file.Mode.Perm() {
					t.Errorf("%s: mode=%v, want %v", file.Path, info.Mode().Perm(), file.Mode.Perm())
				}
				data := matrixRead(t, path)
				if file.Path == "api/go.mod" || file.Path == "api/go.sum" || slices.Contains(want.Conflicts, file.Path) {
					continue
				}
				// Edits survive unless both sides changed the file. Those merges have checks below.
				edited, err := os.ReadFile(filepath.Join(fixture, "edits", filepath.FromSlash(file.Path)))
				switch {
				case err == nil && file.Path != "deploy/compose.dev.yml":
					if !bytes.Equal(data, edited) {
						t.Errorf("%s: user edit was lost", file.Path)
					}
				case os.IsNotExist(err):
					if !bytes.Equal(data, file.Data) {
						t.Errorf("%s: target content differs", file.Path)
					}
				case err != nil:
					t.Fatal(err)
				}
			}
			routes := string(matrixRead(t, filepath.Join(work, "api/internal/routes/routes.go")))
			for _, fragment := range []string{"GET /custom", "// Package routes registers the user application routes.", "// Package routes registers the upgraded application routes."} {
				if !strings.Contains(routes, fragment) {
					t.Errorf("routes lost %q", fragment)
				}
			}
			compose := string(matrixRead(t, filepath.Join(work, "deploy/compose.dev.yml")))
			for _, fragment := range []string{"POSTGRES_PORT:-55432", "interval: 10s"} {
				if !strings.Contains(compose, fragment) {
					t.Errorf("Compose lost %q", fragment)
				}
			}
			if !strings.Contains(string(matrixRead(t, filepath.Join(work, "api/go.mod"))), filepath.ToSlash(filepath.Join(repo, "kit"))) {
				t.Error("local kit replace was lost")
			}
			matrixPaths(t, "remaining conflicts", matrixConflicts(t, work), nil)
			if status := matrixCommand(t, ctx, work, "git", "status", "--porcelain"); status != "" {
				t.Errorf("upgrade worktree is dirty: %s", status)
			}
			if status := matrixCommand(t, ctx, root, "git", "status", "--porcelain"); status != "" {
				t.Errorf("main checkout is dirty: %s", status)
			}
			if !bytes.Equal(originalLock, matrixRead(t, filepath.Join(root, "spinup.lock"))) {
				t.Error("main checkout lock changed")
			}
			if _, err := os.Stat(filepath.Join(root, ".spinup/upgrade.json")); !os.IsNotExist(err) {
				t.Fatalf("upgrade state remains: %v", err)
			}
			if os.Getenv("SPINUP_MATRIX_BUILD") == "1" {
				for _, command := range []string{"build", "vet", "test"} {
					matrixCommand(t, ctx, filepath.Join(work, "api"), "go", command, "./...")
				}
			}
		})
	}
}

// TestLockSchemaMigration preserves the complete M1 lock while adding schema 2 packs.
func TestLockSchemaMigration(t *testing.T) {
	t.Parallel()
	fixtures := filepath.Join("..", "..", "testdata", "corpus", "locks")
	lock, err := manifest.Load(filepath.Join(fixtures, "v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Schema != 2 || !slices.Equal(lock.Packs, []string{"base", "api", "ci"}) {
		t.Fatalf("schema=%d packs=%v", lock.Schema, lock.Packs)
	}
	got, err := lock.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if want := matrixRead(t, filepath.Join(fixtures, "v2.json")); !bytes.Equal(got, want) {
		t.Fatalf("migrated lock differs from v2.json\ngot:\n%s", got)
	}
}
