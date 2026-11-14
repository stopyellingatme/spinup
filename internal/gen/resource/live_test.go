package resource

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spf13/pflag"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	livegen "github.com/stopyellingatme/spinup/internal/gen/live"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func liveFlags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	flags := flagSet(t)
	flags.Bool("live", false, "")
	flags.Bool("mcp", false, "")
	if err := flags.Parse(args); err != nil {
		t.Fatal(err)
	}
	return flags
}

func TestLiveResource(t *testing.T) {
	t.Parallel()
	for _, audit := range []bool{false, true} {
		for _, shared := range []bool{false, true} {
			t.Run(fmt.Sprintf("audit=%t/shared=%t", audit, shared), func(t *testing.T) {
				root := authApp(t)
				packs := []string{"base", "api", "auth", "live"}
				if audit {
					packs = append(packs, "audit")
					write(t, root, map[string]string{"api/db/kit/00006_audit_events.sql": "-- +goose Up\nCREATE TABLE audit_events (id bigint);\n"})
				}
				writeLock(t, root, packs)
				flags := []string{"--live", "--api-only"}
				if shared {
					flags = append(flags, "--shared")
				}
				out, err := run(t.Context(), gen.Input{Root: root, Lock: &manifest.Lock{Packs: packs}, Args: []string{"note", "body:text", "data:json"}, Flags: liveFlags(t, flags...)})
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{"api/internal/handlers/notes.go", "api/internal/handlers/notes_test.go", livegen.GeneratedPath, "api/internal/live/notes.go"} {
					file, ok := out.Plan.Get(path)
					if !ok {
						t.Fatalf("missing %s", path)
					}
					if _, err := parser.ParseFile(token.NewFileSet(), path, file.Data, parser.AllErrors); err != nil {
						t.Fatal(err)
					}
				}
				handler, _ := out.Plan.Get("api/internal/handlers/notes.go")
				source := string(handler.Data)
				tx := "db.InTx"
				if audit {
					tx = "audit.InTx"
				}
				if strings.Count(source, tx+"(ctx, s.DB") != 3 || strings.Count(source, "live.Publish(ctx, tx,") != 3 {
					t.Fatal("missing transactional publishing:\n", source)
				}
				if !strings.Contains(source, "if deleted > 0") || !strings.Contains(source, "item, err = noteFromStore(row)") {
					t.Fatal("missing conversion or delete guard")
				}
				if !slices.Contains(out.Edits, livegen.GeneratedPath) {
					t.Fatal("missing managed edit")
				}
				tests, _ := out.Plan.Get("api/internal/handlers/notes_test.go")
				if !strings.Contains(string(tests.Data), "SELECT count(*) FROM live_outbox WHERE feed = $1") {
					t.Fatal("missing outbox assertion")
				}
				for _, file := range out.Plan.Files {
					write(t, root, map[string]string{file.Path: string(file.Data)})
				}
				retrofit, err := runLive(t.Context(), gen.Input{Root: root, Lock: &manifest.Lock{Packs: packs}, Args: []string{"note"}, Flags: liveFlags(t, "--force")})
				if err != nil {
					t.Fatal(err)
				}
				next := strings.Join(retrofit.Next, "\n")
				key := `live.OwnedKey("notes", user.ID)`
				if shared {
					key = `live.SharedKey("notes")`
				}
				if !strings.Contains(next, key) || !strings.Contains(next, tx+"(ctx, s.DB") || strings.Contains(next, "liveRefresh") {
					t.Fatalf("incorrect instructions: %s", next)
				}

			})
		}
	}
}

func TestExistingLiveSpec(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--api-only"}, {"--api-only", "--shared"}, {"--api-only", "--role", "manager,staff"}} {
		root := authApp(t)
		out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "body:text"}, Flags: flagSet(t, args...)})
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range out.Plan.Files {
			write(t, root, map[string]string{file.Path: string(file.Data)})
		}
		spec, err := ExistingSpec(root, "note")
		if err != nil {
			t.Fatal(err)
		}
		shared := slices.Contains(args, "--shared")
		if spec.Owned == shared || spec.Shared != shared || len(spec.Fields) != 0 || spec.Audit.On {
			t.Fatalf("spec: %+v", spec)
		}
		if slices.Contains(args, "--role") && !slices.Equal(spec.Roles, []string{"manager", "staff"}) {
			t.Fatalf("roles: %v", spec.Roles)
		}
		if err := os.Remove(filepath.Join(root, "api/internal/handlers/notes.go")); err != nil {
			t.Fatal(err)
		}
		if _, err := ExistingSpec(root, "note"); err == nil || !strings.Contains(err.Error(), "Try: spinup g resource note <field:type>...") {
			t.Fatalf("missing handler: %v", err)
		}
	}
}

func TestLiveGenerator(t *testing.T) {
	t.Parallel()
	root := authApp(t)
	writeLock(t, root, []string{"base", "api", "auth", "live"})
	in := gen.Input{Root: root, Lock: &manifest.Lock{Packs: []string{"live"}}, Args: []string{"note"}, Flags: liveFlags(t)}
	if _, err := runLive(t.Context(), gen.Input{Args: []string{"note"}}); err == nil || !strings.Contains(err.Error(), "Try: spinup module add live") {
		t.Fatalf("no pack: %v", err)
	}
	if _, err := runLive(t.Context(), in); err == nil || !strings.Contains(err.Error(), "Try: spinup g resource note") {
		t.Fatalf("no resource: %v", err)
	}
	out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "body:text"}, Flags: flagSet(t, "--api-only")})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range out.Plan.Files {
		write(t, root, map[string]string{file.Path: string(file.Data)})
	}
	write(t, root, map[string]string{"web/src/routes/notes/+page.svelte": "<p>Mine</p>"})
	result, err := runLive(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plan.Files) != 2 {
		t.Fatalf("files: %v", result.Plan.Paths())
	}
	next := strings.Join(result.Next, "\n")
	for _, want := range []string{"if the live module is new: spinup db migrate up", "db.InTx(ctx, s.DB", `live.OwnedKey("notes", user.ID)`, `live.Upsert(strconv.FormatInt(row.ID, 10), item)`, `live.Delete(strconv.FormatInt(req.Id, 10))`, "liveRefresh('notes');"} {
		if !strings.Contains(next, want) {
			t.Errorf("Next lacks %s", want)
		}
	}
	for _, file := range result.Plan.Files {
		write(t, root, map[string]string{file.Path: string(file.Data)})
	}
	if _, err := runLive(t.Context(), in); err == nil || !strings.Contains(err.Error(), "Try: pass --force to replace the file") {
		t.Fatalf("exists: %v", err)
	}
	in.Flags = liveFlags(t, "--force")
	if _, err := runLive(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		packs []string
		want  string
	}{{[]string{"auth"}, "Try: spinup module add live"}, {[]string{"live"}, "Try: spinup module add auth"}} {
		app := freshApp(t)
		writeLock(t, app, tc.packs)
		_, err := run(t.Context(), gen.Input{Root: app, Lock: &manifest.Lock{Packs: tc.packs}, Args: []string{"thing", "body:text"}, Flags: liveFlags(t, "--live", "--api-only")})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("error: %v", err)
		}
	}
}

func TestGoldenGenResourceLive(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "gen-resource-live") {
		return
	}
	root := renderApp(t, []string{"base", "api", "web", "auth", "live"}, []string{"auth", "live"})
	render := func() (fs.FS, error) {
		out, err := run(t.Context(), gen.Input{Root: root, Lock: &manifest.Lock{Packs: []string{"live"}}, Args: []string{"note", "body:text"}, Flags: liveFlags(t, "--live")})
		if err != nil {
			return nil, err
		}
		got := fstest.MapFS{}
		for _, path := range []string{"api/internal/handlers/notes.go", "api/internal/handlers/notes_test.go", livegen.GeneratedPath, "api/internal/live/notes.go", "web/src/routes/notes/+page.svelte"} {
			file, ok := out.Plan.Get(path)
			if !ok {
				t.Fatalf("missing %s", path)
			}
			got[path] = &fstest.MapFile{Data: file.Data, Mode: 0644}
		}
		return got, nil
	}
	got, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "gen-resource-live", got)
	testutil.Deterministic(t, render)
}

// TestPlanLiveDestroy checks that the deletion of a live resource rebuilds
// feeds_gen.go without the removed feed (destroy calls PlanLiveDestroy).
func TestPlanLiveDestroy(t *testing.T) {
	t.Parallel()
	root := authApp(t)
	packs := []string{"base", "api", "auth", "live"}
	writeLock(t, root, packs)
	for _, name := range []string{"note", "memo"} {
		out, err := run(t.Context(), gen.Input{Root: root, Lock: &manifest.Lock{Packs: packs}, Args: []string{name, "body:text"}, Flags: liveFlags(t, "--live", "--api-only")})
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range out.Plan.Files {
			write(t, root, map[string]string{file.Path: string(file.Data)})
		}
	}
	name, err := fields.ParseName("note")
	if err != nil {
		t.Fatal(err)
	}
	var plan vfs.Plan
	for _, path := range Paths(name) {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			if err := plan.Add(vfs.File{Path: path, Delete: true}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, ok := plan.Get(livegen.FeedPath(name)); !ok {
		t.Fatalf("Paths does not delete the feed file: %v", plan.Paths())
	}
	got, err := PlanLiveDestroy(root, plan)
	if err != nil {
		t.Fatal(err)
	}
	generated, ok := got.Get(livegen.GeneratedPath)
	if !ok || generated.Delete || generated.Class != string(manifest.Managed) {
		t.Fatalf("the plan does not rebuild %s: %v", livegen.GeneratedPath, got.Paths())
	}
	if text := string(generated.Data); strings.Contains(text, "registerNotesFeed") || !strings.Contains(text, "registerMemosFeed(mux, h, d)") {
		t.Fatalf("registrations after the destroy:\n%s", text)
	}
	// A plan that deletes no feed file stays the same.
	other := vfs.Plan{Files: []vfs.File{{Path: QueriesPath(name), Delete: true}}}
	if same, err := PlanLiveDestroy(root, other); err != nil || !slices.Equal(same.Paths(), other.Paths()) {
		t.Fatalf("a plan with no feed deletion: %v, %v", same.Paths(), err)
	}
	// A project with no live registrations stays the same.
	if same, err := PlanLiveDestroy(authApp(t), plan); err != nil || !slices.Equal(same.Paths(), plan.Paths()) {
		t.Fatalf("a project with no feeds_gen.go: %v, %v", same.Paths(), err)
	}
}
