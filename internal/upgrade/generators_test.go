package upgrade

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/flags"
	"github.com/stopyellingatme/spinup/internal/gen/mcp"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func generatorFixture(t *testing.T) (*Runner, string) {
	t.Helper()
	r, root := runFixture(t, false)
	for _, version := range r.Source.(chainSource) {
		files := version.Packs.(fstest.MapFS)
		files["flags/pack.toml"] = &fstest.MapFile{Data: []byte("name='flags'\n[[files]]\npath='" + project.FlagsFilePath + "'\nsource='flags.go'\nclass='managed'\n[[files]]\npath='" + project.FlagsTSFilePath + "'\nsource='flags.ts'\nclass='managed'\n")}
		files["flags/flags.go"] = &fstest.MapFile{Data: []byte("// " + version.Version + "\npackage flags\n")}
		files["flags/flags.ts"] = &fstest.MapFile{Data: []byte("// " + version.Version + "\nexport {};\n")}
		files["mcp/pack.toml"] = &fstest.MapFile{Data: []byte("name='mcp'\n[[files]]\npath='" + mcp.GeneratedPath + "'\nsource='tools.go'\nclass='managed'\n")}
		files["mcp/tools.go"] = &fstest.MapFile{Data: []byte("// " + version.Version + "\npackage mcp\n")}
		files["auth/pack.toml"] = &fstest.MapFile{Data: []byte("name='auth'\n[[files]]\npath='" + project.RolesFilePath + "'\nsource='roles.go'\nclass='managed'\n")}
		files["auth/roles.go"] = &fstest.MapFile{Data: project.RenderRolesFile((*project.Config)(nil).AuthRoles())}
	}
	lock, err := r.lock()
	if err != nil {
		t.Fatal(err)
	}
	lock.Packs = append(lock.Packs, "flags", "mcp", "auth")
	writePlanFile(t, root, project.ConfigFile, "[flags.dashboard]\ndescription='The dashboard'\ndefault=true\npublic=true\n", 0644)
	// This tool belongs to the app and has no lock entry. Preview must still scan it.
	writePlanFile(t, root, "api/internal/mcp/dashboard.go", "package mcp\n// spinup:mcp-tool registerDashboard\nfunc registerDashboard() {}\n", 0644)
	output, err := flags.Plan(t.Context(), gen.Input{Root: root, Lock: lock})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := mcp.RenderGenerated(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	output.Plan.Files = append(output.Plan.Files, tool, vfs.File{Path: project.RolesFilePath, Data: project.RenderRolesFile((*project.Config)(nil).AuthRoles())})
	rendered, err := Render(r.Source.(chainSource)["0.1.0"], lock.Packs, lock.Answers)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range output.Plan.Files {
		writePlanFile(t, root, file.Path, string(file.Data), 0644)
		pack, _ := rendered.Get(file.Path)
		// Simulate the stale pack hash after a manual generator run. Bytes already match
		// the required output, so regeneration must record them even without a change.
		lock.Files[file.Path] = manifest.FileEntry{Class: manifest.Managed, Template: pack.Template, Hash: manifest.Hash(pack.Data), Mode: "0644"}
	}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "Add configured generators")
	return r, root
}

func TestUpgradeGeneratorsRepairUnchangedHashes(t *testing.T) {
	t.Parallel()
	r, root := generatorFixture(t)
	report, err := r.Plan(t.Context(), Options{To: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range report.Steps[0].Files {
		if file.Path == project.FlagsFilePath || file.Path == project.FlagsTSFilePath || file.Path == mcp.GeneratedPath {
			if file.Action != ActionUnchanged {
				t.Errorf("%s: action=%s, want unchanged", file.Path, file.Action)
			}
		}
	}
	if err := r.Run(t.Context(), Options{To: "0.2.0", Yes: true, NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, ".spinup/upgrade/0.2.0")
	lock, err := manifest.Load(filepath.Join(work, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{project.FlagsFilePath, project.FlagsTSFilePath, mcp.GeneratedPath} {
		original, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(work, path))
		if err != nil || !bytes.Equal(got, original) || lock.Files[path].Hash != manifest.Hash(got) {
			t.Errorf("%s: output=%q entry=%+v err=%v", path, got, lock.Files[path], err)
		}
	}
	if status := testGit(t, work, "status", "--porcelain"); status != "" {
		t.Fatalf("upgrade left uncommitted output: %s", status)
	}
}

func TestUpgradeGeneratorFailureStopsPreview(t *testing.T) {
	t.Parallel()
	r, root := generatorFixture(t)
	writePlanFile(t, root, project.ConfigFile, "[flags.invalid-key]\n", 0644)
	_, err := r.Plan(t.Context(), Options{To: "0.2.0"})
	if err == nil || !strings.Contains(err.Error(), "upgrade generator flags failed") || !strings.Contains(err.Error(), "Try:") || !strings.Contains(err.Error(), "spinup g flags") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
		t.Fatalf("preview created upgrade state: %v", err)
	}
}

func TestUpgradeRegeneratesAfterCodemodsAndContinues(t *testing.T) {
	old := CurrentPhases()
	defer SetPhases(old)
	r, root := generatorFixture(t)
	verified := false
	SetPhases(Phases{
		After: func(_ context.Context, s *Step) error {
			writePlanFile(t, s.Worktree, project.ConfigFile, "[flags.invalid-key]\n", 0644)
			return nil
		},
		Verify: func(_ context.Context, s *Step) error {
			verified = true
			data, err := os.ReadFile(filepath.Join(s.Worktree, project.FlagsFilePath))
			if err != nil || !bytes.Contains(data, []byte("resolved_flag")) || s.Lock.Files[project.FlagsFilePath].Hash != manifest.Hash(data) {
				t.Fatalf("verification saw stale output: %s, err=%v", data, err)
			}
			return nil
		},
	})
	err := r.Run(t.Context(), Options{To: "0.2.0", Yes: true})
	if err == nil || !strings.Contains(err.Error(), "spinup g flags") || verified {
		t.Fatalf("error=%v verified=%t", err, verified)
	}
	writePlanFile(t, filepath.Join(root, ".spinup/upgrade/0.2.0"), project.ConfigFile, "[flags.resolved_flag]\ndescription='The resolved flag'\ndefault=true\n", 0644)
	if err := r.Continue(t.Context(), Options{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("continue skipped verification")
	}
}

func TestUpgradeRegeneratesAfterConfigurationConflict(t *testing.T) {
	t.Parallel()
	r, root := generatorFixture(t)
	for _, version := range r.Source.(chainSource) {
		files := version.Packs.(fstest.MapFS)
		files["base/pack.toml"].Data = append(files["base/pack.toml"].Data, []byte("\n[[files]]\npath='spinup.toml'\nsource='config.toml'\nclass='scaffold'\n")...)
		files["base/config.toml"] = &fstest.MapFile{Data: []byte("[flags.dashboard]\ndescription='" + version.Version + "'\n")}
	}
	lock, err := r.lock()
	if err != nil {
		t.Fatal(err)
	}
	// Record the last generated output before testing configuration conflict recovery.
	// Stale hash repair has separate coverage in TestUpgradeGeneratorsRepairUnchangedHashes.
	for _, path := range []string{project.FlagsFilePath, project.FlagsTSFilePath} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		entry := lock.Files[path]
		entry.Hash = manifest.Hash(data)
		lock.Files[path] = entry
	}
	oldConfig := r.Source.(chainSource)["0.1.0"].Packs.(fstest.MapFS)["base/config.toml"].Data
	lock.Files[project.ConfigFile] = manifest.FileEntry{Class: manifest.Scaffold, Template: "base/config.toml", Hash: manifest.Hash(oldConfig), Mode: "0644"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "Track configuration")
	if err := r.Run(t.Context(), Options{To: "0.2.0", Yes: true, NoVerify: true}); err == nil || !strings.Contains(err.Error(), "Upgrade has conflicts") {
		t.Fatalf("error=%v", err)
	}
	state, err := loadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Conflicts) != 1 || state.Conflicts[0] != project.ConfigFile {
		t.Fatalf("generated outputs need unnecessary conflict resolution: %v", state.Conflicts)
	}
	writePlanFile(t, state.Worktree, project.ConfigFile, "[flags.resolved_flag]\ndescription='Resolved'\n", 0644)
	if err := r.Continue(t.Context(), Options{Yes: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(state.Worktree, project.FlagsFilePath))
	if err != nil || !bytes.Contains(data, []byte("resolved_flag")) {
		t.Fatalf("output after conflict resolution=%s error=%v", data, err)
	}
}

func TestUpgradeGeneratedOwnerEdits(t *testing.T) {
	for _, path := range []string{project.FlagsFilePath, project.RolesFilePath, mcp.GeneratedPath} {
		for _, force := range []bool{false, true} {
			t.Run(path+"/force="+strconv.FormatBool(force), func(t *testing.T) {
				r, root := generatorFixture(t)
				data, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				owner := []byte(strings.ReplaceAll(string(data)+"// Owner edit.\n", "\n", "\r\n"))
				writePlanFile(t, root, path, string(owner), 0644)
				testGit(t, root, "add", ".")
				testGit(t, root, "commit", "-m", "Edit generated output")
				o := Options{To: "0.2.0", Yes: true, NoVerify: true, ForceManaged: force}
				report, err := r.Plan(t.Context(), o)
				if err != nil {
					t.Fatal(err)
				}
				want := ActionConflict
				if force {
					want = ActionOverwrite
				}
				for _, file := range report.Steps[0].Files {
					if file.Path == path && file.Action != want {
						t.Errorf("%s: action=%s, want %s", path, file.Action, want)
					}
				}
				err = r.Run(t.Context(), o)
				if force {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					command := "spinup g flags"
					switch path {
					case mcp.GeneratedPath:
						command = "spinup g mcp tool"
					case project.RolesFilePath:
						command = "spinup g roles"
					}
					var rendered bytes.Buffer
					ui.New(nil, nil, &rendered, ui.Options{}).RenderError(err)
					if ui.ExitCode(err) != ui.ExitConflicts || !strings.Contains(rendered.String(), "Try:") || !strings.Contains(rendered.String(), command) || !strings.Contains(rendered.String(), "--force-managed") {
						t.Errorf("error=%v output=%s", err, &rendered)
					}
				}
				work := filepath.Join(root, ".spinup/upgrade/0.2.0")
				got, err := os.ReadFile(filepath.Join(work, path))
				if err != nil {
					t.Fatal(err)
				}
				if force && !bytes.Equal(got, data) || !force && !bytes.Equal(got, owner) {
					t.Errorf("output=%s", got)
				}
				if !force {
					state, err := loadState(root)
					if err != nil {
						t.Fatal(err)
					}
					if state.Lock.Files[path].Hash == manifest.Hash(owner) {
						t.Error("owner edit recorded as generated content")
					}
					// Keeping the owner's content is also a valid resolution.
					if err := r.Continue(t.Context(), Options{Yes: true}); err != nil {
						t.Fatal(err)
					}
					got, err = os.ReadFile(filepath.Join(work, path))
					if err != nil || !bytes.Equal(got, owner) {
						t.Errorf("continued output=%s error=%v", got, err)
					}
				}
			})
		}
	}
}

func TestUpgradeGeneratedEjectedFile(t *testing.T) {
	t.Parallel()
	r, root := generatorFixture(t)
	path := mcp.GeneratedPath
	lock, err := r.lock()
	if err != nil {
		t.Fatal(err)
	}
	entry := lock.Files[path]
	entry.Class = manifest.Scaffold
	lock.Files[path] = entry
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	owner := "package mcp\n// Owner registrations.\n"
	writePlanFile(t, root, path, owner, 0644)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "Eject registrations")
	o := Options{To: "0.2.0", Yes: true, NoVerify: true, ForceManaged: true}
	report, err := r.Plan(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range report.Steps[0].Files {
		if file.Path == path && (file.Action != ActionKeep || !strings.Contains(file.Reason, "ejected")) {
			t.Errorf("decision=%+v", file)
		}
	}
	if err := r.Run(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, ".spinup/upgrade/0.2.0")
	got, err := os.ReadFile(filepath.Join(work, path))
	if err != nil || string(got) != owner {
		t.Errorf("ejected output=%s error=%v", got, err)
	}
	next, err := manifest.Load(filepath.Join(work, "spinup.lock"))
	if err != nil || next.Files[path] != entry {
		t.Errorf("ejected lock=%v error=%v", next, err)
	}
}

func TestUpgradeGeneratedTrackedContent(t *testing.T) {
	for _, source := range []string{"lock", "pack"} {
		t.Run(source, func(t *testing.T) {
			r, root := generatorFixture(t)
			lock, err := r.lock()
			if err != nil {
				t.Fatal(err)
			}
			path := project.FlagsFilePath
			entry := lock.Files[path]
			data := []byte("package flags\n// Previously generated.\n")
			if source == "pack" {
				data = r.Source.(chainSource)["0.2.0"].Packs.(fstest.MapFS)["flags/flags.go"].Data
			} else {
				entry.Hash = manifest.Hash(data)
				lock.Files[path] = entry
			}
			writePlanFile(t, root, path, string(data), 0644)
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-m", "Track previous output")
			if err := r.Run(t.Context(), Options{To: "0.2.0", Yes: true, NoVerify: true}); err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(root, ".spinup/upgrade/0.2.0")
			got, err := os.ReadFile(filepath.Join(work, path))
			if err != nil || !bytes.Contains(got, []byte("dashboard")) {
				t.Fatalf("generated output=%s error=%v", got, err)
			}
			next, err := manifest.Load(filepath.Join(work, "spinup.lock"))
			if err != nil || next.Files[path].Hash != manifest.Hash(got) {
				t.Errorf("generated lock=%v error=%v", next, err)
			}
		})
	}
}

func TestUpgradeGeneratedAbsentModule(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlanFile(t, root, project.ConfigFile, "[flags.invalid-key]\n", 0644)
	owner := "package flags\n// No flags module.\n"
	writePlanFile(t, root, project.FlagsFilePath, owner, 0644)
	lock := manifest.New()
	lock.Packs = []string{"base"}
	lock.Files[project.FlagsFilePath] = manifest.FileEntry{Class: manifest.Managed, Template: "flags/flags.go", Hash: manifest.Hash([]byte(owner)), Mode: "0644"}
	plan := &Plan{Files: []FileDecision{{Path: project.FlagsFilePath, Action: ActionKeep}}}
	if err := applyGeneratedPlan(t.Context(), root, lock, plan, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, project.FlagsFilePath))
	if err != nil || string(got) != owner {
		t.Errorf("absent module output=%s error=%v", got, err)
	}
}

func TestUpgradeGeneratedEditAfterCodemods(t *testing.T) {
	old := CurrentPhases()
	defer SetPhases(old)
	r, root := generatorFixture(t)
	owner := "package flags\n// Owner edit during the upgrade.\n"
	SetPhases(Phases{After: func(_ context.Context, s *Step) error {
		writePlanFile(t, s.Worktree, project.FlagsFilePath, owner, 0644)
		return nil
	}})
	if err := r.Run(t.Context(), Options{To: "0.2.0", Yes: true, NoVerify: true}); ui.ExitCode(err) != ui.ExitConflicts {
		t.Fatalf("error=%v", err)
	}
	state, err := loadState(root)
	if err != nil || state.Status != "after-conflicts" {
		t.Fatalf("state=%+v error=%v", state, err)
	}
	if err := r.Continue(t.Context(), Options{Yes: true}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(state.Worktree, project.FlagsFilePath))
	if err != nil || string(got) != owner {
		t.Errorf("continued output=%s error=%v", got, err)
	}
}
