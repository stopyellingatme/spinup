package upgrade

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/doctor"
	_ "github.com/stopyellingatme/spinup/internal/doctor/auditwired"
	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/project"
)

const (
	auditMigrationPath = "api/db/kit/00006_audit_events.sql"
	auditMainPath      = "api/cmd/api/main.go"
)

// TestAuditUpgradeCorpus runs the cases of ADR 0025 decision 15.6 through the real planner
// and the real Git merger. Each case starts from the frozen app in testdata/corpus/audit/old,
// applies the app edits, and upgrades to a template that adds the auth role migration, the
// audit migration, and the audit wiring in main.go.
func TestAuditUpgradeCorpus(t *testing.T) {
	t.Parallel()
	base := filepath.Join("..", "..", "testdata", "corpus", "audit")
	for _, name := range []string{"main-conflict", "migration-clash", "resource-chain", "todo-shape"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := filepath.Join(base, name)
			var want struct {
				Conflicts     []string
				Reasons       map[string][]string
				Order         []string
				NoWarning     bool   `json:"no_warning"`
				DoctorWarning string `json:"doctor_warning"`
			}
			if err := json.Unmarshal(authCorpusRead(t, filepath.Join(fixture, "case.json")), &want); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS(filepath.Join(base, "old"))); err != nil {
				t.Fatal(err)
			}
			oldSpecs := auditCorpusSpecs(t, root)
			lock := planLock("", "")
			old := planVersion("0.1.0", oldSpecs...)
			rendered, err := Render(old, lock.Packs, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range rendered.Files {
				lock.Files[file.Path] = manifest.FileEntry{Template: file.Template, Class: manifest.Class(file.Class), Hash: manifest.Hash(file.Data), Mode: "0644"}
			}
			edits := map[string][]byte{}
			err = filepath.WalkDir(filepath.Join(fixture, "edits"), func(path string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				rel, err := filepath.Rel(filepath.Join(fixture, "edits"), path)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				edits[rel] = authCorpusRead(t, path)
				writePlanFile(t, root, rel, string(edits[rel]), 0o644)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// The new template wires main.go, adds the role migration of auth 00005, and adds the audit migration.
			newMain := authCorpusRead(t, filepath.Join(base, "new", auditMainPath))
			roleMigration := authCorpusRead(t, "../templates/packs/auth/files/"+roleMigrationPath)
			auditMigration := authCorpusRead(t, "../templates/packs/audit/files/"+auditMigrationPath)
			nextSpecs := slices.Clone(oldSpecs)
			for i := range nextSpecs {
				if nextSpecs[i].path == auditMainPath {
					nextSpecs[i].data = string(newMain)
				}
			}
			nextSpecs = append(nextSpecs,
				planSpec{path: roleMigrationPath, data: string(roleMigration), class: "seed"},
				planSpec{path: auditMigrationPath, data: string(auditMigration), class: "seed"})
			plan, err := BuildPlan(t.Context(), PlanInput{Root: root, Lock: lock, Old: old, New: planVersion("0.2.0", nextSpecs...), Merger: merge.NewGit(execx.OSRunner{})})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(plan.Conflicts(), want.Conflicts) {
				t.Fatalf("conflicts = %v, want %v", plan.Conflicts(), want.Conflicts)
			}
			var out bytes.Buffer
			if err := plan.Format(&out); err != nil {
				t.Fatal(err)
			}
			if want.NoWarning && (strings.Contains(out.String(), "Warning:") || strings.Contains(out.String(), "Try:")) {
				t.Fatalf("unexpected warning:\n%s", &out)
			}
			index := map[string]int{}
			for i, file := range plan.Files {
				index[file.Path] = i
				if file.Path == roleMigrationPath || file.Path == auditMigrationPath {
					if file.Action != ActionCreate {
						t.Errorf("%s: %s, want create", file.Path, file.Action)
					}
				}
				if file.Path == "api/internal/handlers/custom.go" && file.Action != ActionUnchanged {
					t.Error("app handler changed")
				}
			}
			for path, parts := range want.Reasons {
				i, ok := index[path]
				if !ok {
					t.Fatalf("plan has no %s", path)
				}
				for _, part := range parts {
					if !strings.Contains(plan.Files[i].Reason, part) {
						t.Errorf("%s: reason %q lacks %q", path, plan.Files[i].Reason, part)
					}
					if !strings.Contains(out.String(), part) {
						t.Errorf("the printed plan lacks %q for %s", part, path)
					}
				}
			}
			if want.NoWarning {
				for _, path := range []string{roleMigrationPath, auditMigrationPath} {
					if reason := plan.Files[index[path]].Reason; reason != "" {
						t.Errorf("%s: unexpected reason %q", path, reason)
					}
				}
			}
			// The pack migrations keep their number order, so the kit applies 00005 then 00006.
			for i := 1; i < len(want.Order); i++ {
				first, ok1 := index[want.Order[i-1]]
				second, ok2 := index[want.Order[i]]
				if !ok1 || !ok2 || first >= second {
					t.Errorf("plan order: %s (%d) must come before %s (%d)", want.Order[i-1], first, want.Order[i], second)
				}
			}
			for path, data := range edits {
				if !bytes.Equal(authCorpusRead(t, filepath.Join(root, filepath.FromSlash(path))), data) {
					t.Errorf("planner changed %s", path)
				}
			}
			if err := writePlan(root, plan); err != nil {
				t.Fatal(err)
			}
			for path, data := range map[string][]byte{roleMigrationPath: roleMigration, auditMigrationPath: auditMigration} {
				if !bytes.Equal(authCorpusRead(t, filepath.Join(root, filepath.FromSlash(path))), data) {
					t.Errorf("upgrade did not create %s", path)
				}
			}
			for path, data := range edits {
				if slices.Contains(want.Conflicts, path) {
					continue
				}
				if !bytes.Equal(authCorpusRead(t, filepath.Join(root, filepath.FromSlash(path))), data) {
					t.Errorf("upgrade changed %s", path)
				}
			}

			// The audit-wired check reads spinup.lock, so the saved copy also names the audit pack.
			doctorLock := *lock
			doctorLock.Packs = append(slices.Clone(lock.Packs), "audit")
			if err := doctorLock.Save(filepath.Join(root, project.LockFile)); err != nil {
				t.Fatal(err)
			}
			if want.DoctorWarning != "" {
				// Resolve the conflict with the app version, which still lacks the audit wiring.
				writePlanFile(t, root, auditMainPath, string(edits[auditMainPath]), 0o644)
				result := auditWiredResult(t, root)
				if result.Status != doctor.Warn || !strings.Contains(result.Message, want.DoctorWarning) || result.Try == "" {
					t.Fatalf("audit-wired = %+v", result)
				}
				// The owner adds the wiring that the Next: lines name. The check passes.
				writePlanFile(t, root, auditMainPath, string(newMain), 0o644)
			}
			if result := auditWiredResult(t, root); result.Status != doctor.OK {
				t.Fatalf("audit-wired after the wiring = %+v", result)
			}
		})
	}
}

// TestAuditModuleAddNextSteps runs spinup module add audit on an app whose migration 00006
// is taken. The preview keeps both spaces and prints the Next: lines for main.go.
func TestAuditModuleAddNextSteps(t *testing.T) {
	t.Parallel()
	r, root := moduleFixture(t, false)
	version := r.Source.(chainSource)["0.1.0"]
	fsys := version.Packs.(fstest.MapFS)
	fsys["audit/pack.toml"] = &fstest.MapFile{Data: []byte("name='audit'\nrequires=['api']\n[[files]]\npath='" + auditMigrationPath + "'\nsource='audit.sql'\nclass='seed'\n")}
	fsys["audit/audit.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")}
	writePlanFile(t, root, "api/db/migrations/00006_create_notes.sql", "-- +goose Up\nSELECT 1;\n", 0o644)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "app migration")
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"audit"}, Options: Options{Yes: true, NoVerify: true}}); err != nil {
		t.Fatal(err)
	}
	out := r.Out.(*bytes.Buffer).String()
	if strings.Contains(out, "also used by") {
		t.Fatalf("unexpected clash warning: %s", out)
	}
	for _, part := range []string{
		"Next: in api/cmd/api/main.go, import:",
		"auth.WithEvents(authsink.New(pool)),",
		`app.WithCommand("audit", audit.Command(pool)),`,
		"Next: run spinup db migrate up before starting the new binary.",
		"Next: run spinup g audit <resource> for each existing table.",
		"git merge spinup/module-audit",
	} {
		if !strings.Contains(out, part) {
			t.Errorf("output lacks %q:\n%s", part, out)
		}
	}
	if status := testGit(t, root, "status", "--porcelain"); status != "" {
		t.Fatalf("main checkout is dirty: %s", status)
	}
}

// auditCorpusSpecs reads the frozen app files under root as template files. SQL files are seeds.
func auditCorpusSpecs(t *testing.T, root string) []planSpec {
	t.Helper()
	var specs []planSpec
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		class := "scaffold"
		if strings.HasSuffix(rel, ".sql") {
			class = "seed"
		}
		specs = append(specs, planSpec{path: rel, data: string(authCorpusRead(t, path)), class: class})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return specs
}

func auditWiredResult(t *testing.T, root string) doctor.Result {
	t.Helper()
	for _, check := range doctor.Checks(doctor.Local, false) {
		if check.ID == "audit-wired" {
			return check.Run(t.Context(), &doctor.Context{Root: root})
		}
	}
	t.Fatal("audit-wired is not registered")
	return doctor.Result{}
}
