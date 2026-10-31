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

	"github.com/stopyellingatme/spinup/internal/doctor"
	_ "github.com/stopyellingatme/spinup/internal/doctor/rolesdrift"
	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestRolesUpgradeCorpus(t *testing.T) {
	t.Parallel()
	base := filepath.Join("..", "..", "testdata", "corpus", "roles")
	for _, name := range []string{"users", "migration", "main-conflict", "custom-auth"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := filepath.Join(base, name)
			var want struct {
				Warning       string
				Conflicts     []string
				DoctorWarning string `json:"doctor_warning"`
			}
			if err := json.Unmarshal(authCorpusRead(t, filepath.Join(fixture, "case.json")), &want); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS(filepath.Join(base, "old"))); err != nil {
				t.Fatal(err)
			}
			var oldSpecs []planSpec
			lock := planLock("", "")
			err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
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
				data := authCorpusRead(t, path)
				oldSpecs = append(oldSpecs, planSpec{path: rel, data: string(data), class: class})
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
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
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(filepath.Join(fixture, "edits"), path)
				if err != nil {
					return err
				}
				data := authCorpusRead(t, path)
				edits[rel] = data
				writePlanFile(t, root, rel, string(data), 0o644)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			nextSpecs := slices.Clone(oldSpecs)
			for i := range nextSpecs {
				if nextSpecs[i].path == "api/cmd/api/main.go" {
					nextSpecs[i].data = string(authCorpusRead(t, filepath.Join(base, "new", nextSpecs[i].path)))
				}
			}
			roleData := project.RenderRolesFile((*project.Config)(nil).AuthRoles())
			migration := authCorpusRead(t, "../templates/packs/auth/files/api/db/migrations/00005_users_role.sql")
			nextSpecs = append(nextSpecs, planSpec{path: project.RolesFilePath, data: string(roleData), class: "managed"}, planSpec{path: roleMigrationPath, data: string(migration), class: "seed"})
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
			if want.Warning != "" && !strings.Contains(out.String(), want.Warning) {
				t.Fatalf("missing %q:\n%s", want.Warning, &out)
			}
			for _, file := range plan.Files {
				if file.Path == project.RolesFilePath || file.Path == roleMigrationPath {
					if file.Action != ActionCreate {
						t.Errorf("%s: %s, want create", file.Path, file.Action)
					}
				}
				if file.Path == project.RolesFilePath && !bytes.Equal(file.Data, roleData) {
					t.Error("custom auth changed the upgrade render")
				}
				if file.Path == "api/internal/handlers/custom.go" && file.Action != ActionUnchanged {
					t.Error("app handler changed")
				}
			}
			if want.DoctorWarning != "" && bytes.Contains(authCorpusRead(t, filepath.Join(root, "api/cmd/api/main.go")), []byte("auth.WithRoles(")) {
				t.Fatal("conflict fixture must need the roles-drift warning")
			}
			for path, data := range edits {
				if !bytes.Equal(authCorpusRead(t, filepath.Join(root, path)), data) {
					t.Errorf("planner changed %s", path)
				}
			}
			if err := writePlan(root, plan); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(authCorpusRead(t, filepath.Join(root, project.RolesFilePath)), roleData) {
				t.Error("upgrade did not create default roles")
			}
			if !bytes.Equal(authCorpusRead(t, filepath.Join(root, roleMigrationPath)), migration) {
				t.Error("upgrade did not create role migration")
			}
			for path, data := range edits {
				if slices.Contains(want.Conflicts, filepath.ToSlash(path)) {
					continue
				}
				if !bytes.Equal(authCorpusRead(t, filepath.Join(root, path)), data) {
					t.Errorf("upgrade changed %s", path)
				}
			}

			if want.DoctorWarning != "" {
				t.Run("roles-drift", func(t *testing.T) {
					// Resolve the conflict with the app version, which still lacks WithRoles.
					writePlanFile(t, root, "api/cmd/api/main.go", string(edits[filepath.FromSlash("api/cmd/api/main.go")]), 0o644)
					// The doctor check reads the modules from spinup.lock. The planner lock has only the
					// synthetic base pack, so the saved copy also names the auth module.
					doctorLock := *lock
					doctorLock.Packs = append(slices.Clone(lock.Packs), "auth")
					if err := doctorLock.Save(filepath.Join(root, project.LockFile)); err != nil {
						t.Fatal(err)
					}
					for _, check := range doctor.Checks(doctor.Local, false) {
						if check.ID != "roles-drift" {
							continue
						}
						result := check.Run(t.Context(), &doctor.Context{Root: root})
						if result.Status != doctor.Warn || !strings.Contains(result.Message, want.DoctorWarning) {
							t.Fatalf("roles-drift = %+v", result)
						}
						return
					}
					t.Fatal("roles-drift is not registered")
				})
			}
		})
	}
}

func authCorpusRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
