package newapp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/ui"
)

type fakePrompter struct {
	keys    []string
	answers map[string]any
}

func (p *fakePrompter) Ask(_ context.Context, q templates.Question, value any) (any, error) {
	p.keys = append(p.keys, q.Key)
	if answer, ok := p.answers[q.Key]; ok {
		return answer, nil
	}
	return value, nil
}

func TestGenerateAndApply(t *testing.T) {
	t.Parallel()
	kit := t.TempDir()
	o := Options{Name: "todo", Dir: t.TempDir(), APIOnly: true, NoGit: true, Version: "dev", KitReplace: kit, Answers: map[string]any{"module": "example.com/todo"}}
	result, err := Generate(context.Background(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Packs, []string{"base", "api", "ci", "deploy"}) {
		t.Fatalf("packs=%q", result.Packs)
	}
	if _, err := os.Stat(result.Dir); !os.IsNotExist(err) {
		t.Fatal("generator wrote files")
	}
	var out, stderr bytes.Buffer
	if err := Apply(context.Background(), result, o, &run.Fake{}, ui.New(nil, &out, &stderr, ui.Options{})); err != nil {
		t.Fatal(err)
	}
	lock, err := manifest.Load(filepath.Join(result.Dir, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Validate(); err != nil {
		t.Fatal(err)
	}
	if lock.Kit.Version != "v0.1.0" || lock.TemplateVersion != "0.0.0-dev" || lock.BehaviorVersion != 1 || len(lock.StackVersions) == 0 {
		t.Fatalf("lock=%#v", lock)
	}
	for name, entry := range lock.Files {
		data, err := os.ReadFile(filepath.Join(result.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if manifest.Hash(data) != entry.Hash {
			t.Errorf("hash differs: %s", name)
		}
	}
	if lock.Files["api/cmd/api/main.go"].Class != manifest.Scaffold {
		t.Fatal("main class")
	}
	if _, ok := lock.Files[".env"]; ok {
		t.Fatal("env is tracked")
	}
	if _, err := os.Stat(filepath.Join(result.Dir, ".env")); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(result.Dir, "api/go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mod), "replace github.com/stopyellingatme/spinup/kit => "+kit) {
		t.Fatal(string(mod))
	}
	if !strings.Contains(out.String(), "Created todo (") || !strings.Contains(out.String(), "Next:") {
		t.Fatal(out.String())
	}
	if _, err := Generate(context.Background(), o, nil); err == nil {
		t.Fatal("accepted occupied directory")
	}
}

func TestGenerateErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		api     bool
		modules string
		want    string
	}{
		{"unknown", true, "nope", "ci"}, {"base module", true, "base", "available modules"}, {"unsafe name", true, "ci", "app_name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := "todo"
			if tc.name == "unsafe name" {
				name = "../todo"
			}
			_, err := Generate(context.Background(), Options{Name: name, Dir: t.TempDir(), APIOnly: tc.api, Answers: map[string]any{"modules": tc.modules}}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestQuestionsAndPrompt(t *testing.T) {
	t.Parallel()
	p := &fakePrompter{}
	o := Options{Name: "todo", Dir: t.TempDir(), APIOnly: true, Interactive: true, Answers: map[string]any{"module": "example.com/todo"}}
	if _, err := Generate(context.Background(), o, p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.keys, []string{"modules", "db_version"}) {
		t.Fatalf("prompt keys=%q", p.keys)
	}
}

func TestModuleQuestionsOnlyForSelectedPacks(t *testing.T) {
	t.Parallel()
	p := &fakePrompter{answers: map[string]any{"modules": "deploy,monitoring", "monitoring_profile": "full"}}
	o := Options{Name: "todo", Dir: t.TempDir(), Interactive: true, Answers: map[string]any{"module": "example.com/todo"}}
	result, err := Generate(context.Background(), o, p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.keys, "monitoring_profile") {
		t.Fatalf("prompt keys=%q, want monitoring_profile", p.keys)
	}
	if got := result.Lock.Answers["monitoring_profile"]; got != "full" {
		t.Fatalf("monitoring_profile=%v", got)
	}
}

func TestOrderPacks(t *testing.T) {
	t.Parallel()
	packs := map[string]*templates.Pack{"base": {Name: "base"}, "api": {Name: "api", Requires: []string{"base"}}, "ci": {Name: "ci"}}
	ordered, err := orderPacks(packs)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range ordered {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"base", "api", "ci"}) {
		t.Fatal(names)
	}
	packs["base"].Requires = []string{"api"}
	if _, err := orderPacks(packs); err == nil {
		t.Fatal("accepted cycle")
	}
	packs["base"].Requires = []string{"missing"}
	if _, err := orderPacks(packs); err == nil {
		t.Fatal("accepted missing pack")
	}
}

func TestTargetChecks(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"empty", "file", "nonempty", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			target := filepath.Join(dir, "todo")
			switch kind {
			case "empty":
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(target, nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "nonempty":
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "keep"), nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), target); err != nil {
					t.Skip(err)
				}
			}
			_, err := Generate(context.Background(), Options{Name: "todo", Dir: dir, APIOnly: true}, nil)
			if (err == nil) != (kind == "empty") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDryRunAndGitFailure(t *testing.T) {
	t.Parallel()
	for _, dry := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry", false: "git failure"}[dry], func(t *testing.T) {
			t.Parallel()
			o := Options{Name: "todo", Dir: t.TempDir(), APIOnly: true, DryRun: dry, Version: "v0.1.0"}
			result, err := Generate(context.Background(), o, nil)
			if err != nil {
				t.Fatal(err)
			}
			fake := &run.Fake{}
			if !dry {
				fake.Expect("git", "init", "-b", "main")
				fake.Expect("git", "add", "-A")
				fake.Expect("git", "commit", "-m", "Initial commit from spinup v0.1.0").Return("", "no identity", 1)
			}
			var out bytes.Buffer
			if err := Apply(context.Background(), result, o, fake, ui.New(nil, &out, &out, ui.Options{})); err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			if dry {
				if _, err := os.Stat(result.Dir); !os.IsNotExist(err) {
					t.Fatal("dry-run wrote directory")
				}
			} else if !strings.Contains(out.String(), "Warning:") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestTemplateVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ version, want string }{
		{"dev", "0.0.0-dev"}, {"branch-sha", "0.0.0-dev"}, {"1.2.3", "1.2.3"}, {"v1.2.3-rc.1+build.4", "v1.2.3-rc.1+build.4"}, {"1.2.3-01", "0.0.0-dev"}, {"01.2.3", "0.0.0-dev"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			if got := templateVersion(tc.version); got != tc.want {
				t.Fatalf("version=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestMissingAnswerWithoutTerminal(t *testing.T) {
	t.Parallel()
	p := &fakePrompter{}
	_, _, err := completeAnswers(context.Background(), Options{Name: "todo"}, []templates.Question{{Key: "required_value", Type: "string"}}, p)
	if err == nil || !strings.Contains(err.Error(), "--required-value") {
		t.Fatalf("error=%v", err)
	}
	if len(p.keys) != 0 {
		t.Fatalf("prompted: %q", p.keys)
	}
}

func TestLockDeterminismAndModuleSelection(t *testing.T) {
	t.Parallel()
	o := Options{Name: "todo", Dir: t.TempDir(), APIOnly: true, Answers: map[string]any{"modules": ""}}
	a, err := Generate(context.Background(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(context.Background(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	one, err := a.Lock.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	two, err := b.Lock.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) || !reflect.DeepEqual(a.Plan, b.Plan) {
		t.Fatal("generation is not deterministic")
	}
	if !reflect.DeepEqual(a.Packs, []string{"base", "api"}) {
		t.Fatalf("packs=%q", a.Packs)
	}
	if len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "--module") {
		t.Fatalf("warnings=%q", a.Warnings)
	}
}

func TestEnvironmentIsNotOverwritten(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	want := []byte("DATABASE_URL=keep\n")
	for name, data := range map[string][]byte{".env": want, ".env.example": []byte("DATABASE_URL=new\n")} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyEnvironment(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("env=%q", got)
	}
}

func TestPromptAnswersAndFlagPrecedence(t *testing.T) {
	t.Parallel()
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{true: "interactive", false: "defaults"}[interactive], func(t *testing.T) {
			t.Parallel()
			p := &fakePrompter{answers: map[string]any{"enabled": false, "mode": "lite", "module": "wrong.example/test"}}
			questions := []templates.Question{{Key: "module", Type: "string"}, {Key: "enabled", Type: "bool", Default: "true"}, {Key: "mode", Type: "enum", Choices: []string{"full", "lite"}, Default: "full"}}
			answers, _, err := completeAnswers(context.Background(), Options{Name: "todo", Interactive: interactive, Answers: map[string]any{"module": "example.com/todo"}}, questions, p)
			if err != nil {
				t.Fatal(err)
			}
			if answers["module"] != "example.com/todo" {
				t.Fatal("prompt replaced explicit flag")
			}
			if interactive {
				if answers["enabled"] != false || answers["mode"] != "lite" || !reflect.DeepEqual(p.keys, []string{"enabled", "mode"}) {
					t.Fatalf("answers=%v, prompts=%q", answers, p.keys)
				}
			} else if answers["enabled"] != true || answers["mode"] != "full" || len(p.keys) != 0 {
				t.Fatalf("answers=%v, prompts=%q", answers, p.keys)
			}
		})
	}
}

func TestGenerateRecordsPacks(t *testing.T) {
	t.Parallel()
	result, err := Generate(context.Background(), Options{Name: "todo", Dir: t.TempDir(), APIOnly: true, Answers: map[string]any{"module": "example.com/todo", "modules": "ci"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Lock.Packs, ","); got != "base,api,ci" {
		t.Fatalf("lock packs = %q, want base,api,ci", got)
	}
}
