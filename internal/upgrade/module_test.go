package upgrade

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func moduleFixture(t *testing.T, collision bool) (*Runner, string) {
	t.Helper()
	r, root := runFixture(t, false)
	version := r.Source.(chainSource)["0.1.0"]
	fsys := version.Packs.(fstest.MapFS)
	fsys["base/pack.toml"].Data = append(fsys["base/pack.toml"].Data, []byte("\n[[questions]]\nkey='modules'\nprompt='Modules'\ntype='string'\ndefault=''\n[[files]]\npath='spinup.toml'\nsource='config.tmpl'\nclass='scaffold'\n")...)
	fsys["base/config.tmpl"] = &fstest.MapFile{Data: []byte("modules = [ [[range .modules]][[quote .]], [[end]] ]\n[app]\nname = 'demo'\nmodule = 'example.com/demo'\n")}
	fsys["api/pack.toml"] = &fstest.MapFile{Data: []byte("name='api'\n")}
	fsys["jobs/pack.toml"] = &fstest.MapFile{Data: []byte("name='jobs'\nrequires=['api']\n[[questions]]\nkey='queue_name'\nprompt='Queue name'\ntype='string'\ndefault='normal'\n[[files]]\npath='job.txt'\nsource='job.tmpl'\nclass='managed'\n")}
	fsys["jobs/job.tmpl"] = &fstest.MapFile{Data: []byte("queue=[[.queue_name]]\n")}
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Packs = []string{"base", "api"}
	lock.Answers = map[string]any{"modules": []string{}}
	rendered, err := Render(version, lock.Packs, lock.Answers)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := rendered.Get("spinup.toml")
	lock.Files["spinup.toml"] = manifest.FileEntry{Class: manifest.Scaffold, Template: config.Template, Hash: manifest.Hash(config.Data), Mode: "0644"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, root, "spinup.toml", "# retained heading\nmodules = [] # retained modules\n[app]\nname = 'demo'\nmodule = 'example.com/demo'\n# retained app\n", 0644)
	if collision {
		writePlanFile(t, root, "job.txt", "user job\n", 0644)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "module fixture")
	return r, root
}

func TestModuleRun(t *testing.T) {
	t.Parallel()
	r, root := moduleFixture(t, false)
	// A retained upgrade branch must not prevent adding modules at the locked version.
	testGit(t, root, "branch", "spinup/upgrade-0.1.0")
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"jobs", "jobs"}, Answers: map[string]any{"queue_name": "priority"}, Options: Options{Yes: true, NoVerify: true}}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, ".spinup/upgrade/0.1.0")
	lock, err := manifest.Load(filepath.Join(work, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.TemplateVersion != "0.1.0" || !slices.Contains(lock.Packs, "jobs") || lock.Answers["queue_name"] != "priority" || lock.Files["job.txt"].Template != "jobs/job.tmpl" {
		t.Fatalf("lock=%+v", lock)
	}
	config, err := os.ReadFile(filepath.Join(work, "spinup.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# retained heading", "# retained modules", "# retained app"} {
		if !bytes.Contains(config, []byte(comment)) {
			t.Fatalf("lost comment: %s", config)
		}
	}
	cfg, err := project.Parse(config)
	if err != nil || !slices.Contains(cfg.Modules, "jobs") {
		t.Fatalf("config=%s error=%v", config, err)
	}
	if status := testGit(t, root, "status", "--porcelain"); status != "" {
		t.Fatalf("main dirty: %s", status)
	}
	if status := testGit(t, work, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree dirty: %s", status)
	}
	if !strings.Contains(r.Out.(*bytes.Buffer).String(), "git merge spinup/module-jobs") {
		t.Fatalf("output=%s", r.Out)
	}
	if got := testGit(t, root, "show", "HEAD:spinup.toml"); strings.Contains(got, `"jobs"`) {
		t.Fatal("main changed")
	}
}

func TestModuleRecovery(t *testing.T) {
	t.Parallel()
	for _, abort := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "abort"}[abort], func(t *testing.T) {
			t.Parallel()
			r, root := moduleFixture(t, true)
			err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"jobs"}, Options: Options{Yes: true, NoVerify: true}})
			if ui.ExitCode(err) != ui.ExitConflicts {
				t.Fatalf("error=%v", err)
			}
			state, err := loadState(root)
			if err != nil || state.Branch != "spinup/module-jobs" {
				t.Fatalf("state=%+v error=%v", state, err)
			}
			// A fresh runner uses only persisted input for --continue and --abort.
			next := &Runner{Root: root, Source: r.Source, Out: r.Out}
			if abort {
				if err := next.Abort(t.Context(), Options{Yes: true}); err != nil {
					t.Fatal(err)
				}
				if got := testGit(t, root, "branch", "--list", "spinup/module-jobs"); got != "" {
					t.Fatalf("branch remains: %s", got)
				}
				if _, err := os.Stat(state.Worktree); !os.IsNotExist(err) {
					t.Fatalf("worktree remains: %v", err)
				}
			} else {
				if err := next.Continue(t.Context(), Options{Yes: true}); ui.ExitCode(err) != ui.ExitConflicts {
					t.Fatalf("unresolved=%v", err)
				}
				writePlanFile(t, state.Worktree, "job.txt", "resolved\n", 0644)
				if err := next.Continue(t.Context(), Options{Yes: true}); err != nil {
					t.Fatal(err)
				}
				lock, err := manifest.Load(filepath.Join(state.Worktree, "spinup.lock"))
				if err != nil || !slices.Contains(lock.Packs, "jobs") || lock.Answers["queue_name"] != "normal" {
					t.Fatalf("lock=%+v error=%v", lock, err)
				}
			}
			if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
				t.Fatalf("state remains: %v", err)
			}
		})
	}
}

func TestModuleInterruptedApply(t *testing.T) {
	previous := CurrentPhases()
	defer SetPhases(previous)
	attempts := 0
	SetPhases(Phases{Apply: func(_ context.Context, s *Step) error {
		attempts++
		if attempts == 1 {
			return errors.New("interrupted module apply")
		}
		return nil
	}})
	r, root := moduleFixture(t, false)
	err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"jobs"}, Answers: map[string]any{"queue_name": "saved"}, Options: Options{Yes: true, NoVerify: true}})
	if err == nil || !strings.Contains(err.Error(), "interrupted module apply") {
		t.Fatalf("error=%v", err)
	}
	if err := r.Continue(t.Context(), Options{Yes: true}); err != nil {
		t.Fatal(err)
	}
	lock, err := manifest.Load(filepath.Join(root, ".spinup/upgrade/0.1.0/spinup.lock"))
	if err != nil || lock.Answers["queue_name"] != "saved" || attempts != 2 {
		t.Fatalf("lock=%v attempts=%d error=%v", lock, attempts, err)
	}
}

func TestModulePreconditions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		dirty, yes, dry bool
		want            string
	}{
		{name: "confirmation", want: "--yes"},
		{name: "dirty", dirty: true, yes: true, want: "uncommitted"},
		{name: "preview", dry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, root := moduleFixture(t, false)
			if tc.dirty {
				writePlanFile(t, root, "user.txt", "user", 0644)
			}
			err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"jobs"}, Options: Options{Yes: tc.yes, DryRun: tc.dry, NoVerify: true}})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
				t.Fatalf("state written: %v", err)
			}
			if got := testGit(t, root, "branch", "--list", "spinup/module-jobs"); got != "" {
				t.Fatalf("branch created: %s", got)
			}
		})
	}
}

func TestModuleStateValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		names  []string
		branch string
		valid  bool
	}{
		{"valid", []string{"jobs"}, "spinup/module-jobs", true},
		{"multiple", []string{"ci", "jobs"}, "spinup/module-ci-jobs", true},
		{"unknown", []string{"bogus"}, "spinup/module-bogus", false},
		{"unsafe", []string{"../jobs"}, "spinup/module-../jobs", false},
		{"core", []string{"web"}, "spinup/module-web", false},
		{"duplicate", []string{"jobs", "jobs"}, "spinup/module-jobs-jobs", false},
		{"wrong branch", []string{"jobs"}, "spinup/module-ci", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &runState{Schema: 1, Chain: []string{"0.1.0"}, Status: "step", Worktree: filepath.Join(root, ".spinup/upgrade/0.1.0"), Branch: tc.branch, Lock: planLock("old", "scaffold"), Module: &moduleChange{Names: tc.names, Packs: []string{"base", "jobs"}, Answers: map[string]any{"modules": []string{"jobs"}}}}
			if err := s.validate(root); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestModuleMonitoringDependencyProfile(t *testing.T) {
	t.Parallel()
	r, root := moduleFixture(t, false)
	fsys := r.Source.(chainSource)["0.1.0"].Packs.(fstest.MapFS)
	fsys["jobs/pack.toml"].Data = bytes.ReplaceAll(fsys["jobs/pack.toml"].Data, []byte("requires=['api']"), []byte("requires=['api','monitoring']"))
	fsys["deploy/pack.toml"] = &fstest.MapFile{Data: []byte("name='deploy'\nrequires=['api']\n")}
	fsys["monitoring/pack.toml"] = &fstest.MapFile{Data: []byte("name='monitoring'\nrequires=['deploy']\n[[questions]]\nkey='monitoring_profile'\nprompt='Monitoring profile'\ntype='enum'\nchoices=['lite','full','none']\ndefault='lite'\n")}
	config := testGit(t, root, "show", "HEAD:spinup.toml") + "\n[monitoring]\nprofile = 'none' # retain profile comment\n"
	writePlanFile(t, root, "spinup.toml", config, 0644)
	testGit(t, root, "commit", "-am", "disabled monitoring profile")
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"jobs"}, Options: Options{Yes: true, NoVerify: true}}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, ".spinup/upgrade/0.1.0")
	cfg, err := project.Load(work)
	if err != nil || cfg.Monitoring.Profile != "none" || !slices.Contains(cfg.Modules, "monitoring") {
		t.Fatalf("configuration=%+v error=%v", cfg, err)
	}
	lock, err := manifest.Load(filepath.Join(work, "spinup.lock"))
	if err != nil || lock.Answers["monitoring_profile"] != "none" {
		t.Fatalf("lock=%+v error=%v", lock, err)
	}
	if !strings.Contains(r.Out.(*bytes.Buffer).String(), "Adding required modules: deploy, monitoring") {
		t.Fatalf("output=%s", r.Out)
	}
	data, err := os.ReadFile(filepath.Join(work, "spinup.toml"))
	if err != nil || !bytes.Contains(data, []byte("# retain profile comment")) {
		t.Fatalf("configuration=%s error=%v", data, err)
	}
}

func TestModuleRepairsDisabledConfig(t *testing.T) {
	t.Parallel()
	r, root := moduleFixture(t, false)
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Packs = append(lock.Packs, "jobs")
	lock.Answers["modules"] = []string{"jobs"}
	lock.Answers["queue_name"] = "normal"
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "commit", "-am", "locked jobs with disabled configuration")
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"jobs"}, Options: Options{Yes: true, NoVerify: true}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Load(filepath.Join(root, ".spinup/upgrade/0.1.0"))
	if err != nil || !slices.Contains(cfg.Modules, "jobs") {
		t.Fatalf("configuration=%+v error=%v", cfg, err)
	}
}
