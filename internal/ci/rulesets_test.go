package ci_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
	"go.yaml.in/yaml/v3"
)

// actionsAppID is the GitHub Actions app, which the required checks must come from.
const actionsAppID = 15368

type rulesetFile struct {
	Name        string `yaml:"name"`
	Enforcement string `yaml:"enforcement"`
	Rules       []struct {
		Type       string `yaml:"type"`
		Parameters struct {
			Checks []struct {
				Context       string `yaml:"context"`
				IntegrationID int    `yaml:"integration_id"`
			} `yaml:"required_status_checks"`
		} `yaml:"parameters"`
	} `yaml:"rules"`
}

type workflowFile struct {
	On   yaml.Node `yaml:"on"`
	Jobs map[string]struct {
		Name string `yaml:"name"`
	} `yaml:"jobs"`
}

// checkRulesets returns one message for each broken rule of the contract between the
// ruleset files and the workflow files. The maps hold file contents by file name.
func checkRulesets(rulesets, workflows map[string][]byte) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	parsed := map[string]workflowFile{}
	for name, data := range workflows {
		var w workflowFile
		if err := yaml.Unmarshal(data, &w); err != nil {
			add("%s: %v", name, err)
			continue
		}
		parsed[name] = w
	}
	ci, ok := parsed["ci.yml"]
	if !ok {
		return append(problems, "ci.yml is missing")
	}
	for _, key := range []string{"paths", "paths-ignore", "branches"} {
		if n := yamlKey(&ci.On, "pull_request"); n != nil && yamlKey(n, key) != nil {
			add("on.pull_request of ci.yml sets %s: a required check can stay pending", key)
		}
	}
	for name, data := range rulesets {
		var r rulesetFile
		if err := yaml.Unmarshal(data, &r); err != nil { // JSON is YAML
			add("%s: %v", name, err)
			continue
		}
		if r.Enforcement != "active" {
			add("%s: enforcement is %q, want active", name, r.Enforcement)
		}
		for _, rule := range r.Rules {
			for _, check := range rule.Parameters.Checks {
				c := check.Context
				if check.IntegrationID != actionsAppID {
					add("%s: check %s has integration_id %d, want %d", name, c, check.IntegrationID, actionsAppID)
				}
				if c == "gate" {
					add("%s: check gate is skipped on a pull request", name)
				}
				job, found := ci.Jobs[c]
				switch {
				case !found:
					add("%s: check %s is not a job id in ci.yml", name, c)
				case job.Name != "":
					add("%s: job %s in ci.yml has a name override %q", name, c, job.Name)
				}
				for wfName, w := range parsed {
					if wfName == "ci.yml" {
						continue
					}
					for id, j := range w.Jobs {
						if id == c || j.Name == c {
							add("%s: job %s in %s has the same check name as %s", name, id, wfName, c)
						}
					}
				}
			}
		}
	}
	slices.Sort(problems)
	return problems
}

// yamlKey returns the value of key in a mapping node, or in a sequence or scalar when the key is a name.
func yamlKey(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func readDir(t *testing.T, dir, glob string) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, glob))
	if err != nil || len(files) == 0 {
		t.Fatalf("no files match %s in %s: %v", glob, dir, err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = data
	}
	return out
}

func TestRulesetsMatchWorkflows(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	rulesets := readDir(t, filepath.Join(root, ".github/rulesets"), "*.json")
	workflows := readDir(t, filepath.Join(root, ".github/workflows"), "*.yml")
	if problems := checkRulesets(rulesets, workflows); len(problems) != 0 {
		t.Fatalf("rulesets and workflows disagree:\n%v", problems)
	}
	merge, ok := rulesets["main-merge.json"]
	if !ok {
		t.Fatal("main-merge.json is missing")
	}
	var r rulesetFile
	if err := yaml.Unmarshal(merge, &r); err != nil {
		t.Fatal(err)
	}
	var contexts []string
	for _, rule := range r.Rules {
		for _, c := range rule.Parameters.Checks {
			contexts = append(contexts, c.Context)
		}
	}
	if want := []string{"actionlint", "lint", "test", "test-macos", "upgrade", "windows"}; !slices.Equal(contexts, want) {
		t.Errorf("required checks %v, want %v", contexts, want)
	}
}

func TestRulesetChecksRejectBrokenInput(t *testing.T) {
	t.Parallel()
	const ruleset = `{"name":"m","enforcement":"%s","rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"%s","integration_id":%d}]}}]}`
	const goodCI = "on:\n  pull_request:\njobs:\n  lint: {}\n  gate: {}\n"
	tests := []struct {
		name        string
		enforcement string
		context     string
		app         int
		ci          string
		other       string
		want        string
	}{
		{"ok", "active", "lint", actionsAppID, goodCI, "jobs:\n  other: {}\n", ""},
		{"disabled", "disabled", "lint", actionsAppID, goodCI, "", "enforcement"},
		{"unknown job", "active", "build", actionsAppID, goodCI, "", "not a job id"},
		{"gate", "active", "gate", actionsAppID, goodCI, "", "gate is skipped"},
		{"wrong app", "active", "lint", 1, goodCI, "", "integration_id"},
		{"name override", "active", "lint", actionsAppID, "on:\n  pull_request:\njobs:\n  lint:\n    name: Lint\n", "", "name override"},
		{"paths filter", "active", "lint", actionsAppID, "on:\n  pull_request:\n    paths: [a]\njobs:\n  lint: {}\n", "", "sets paths"},
		{"same name elsewhere", "active", "lint", actionsAppID, goodCI, "jobs:\n  x:\n    name: lint\n", "same check name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			workflows := map[string][]byte{"ci.yml": []byte(tt.ci)}
			if tt.other != "" {
				workflows["other.yml"] = []byte(tt.other)
			}
			rulesets := map[string][]byte{"m.json": fmt.Appendf(nil, ruleset, tt.enforcement, tt.context, tt.app)}
			problems := checkRulesets(rulesets, workflows)
			if tt.want == "" {
				if len(problems) != 0 {
					t.Fatalf("unexpected problems: %v", problems)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatalf("no problem reported, want one with %q", tt.want)
			}
			for _, p := range problems {
				if strings.Contains(p, tt.want) {
					return
				}
			}
			t.Fatalf("problems %v do not mention %q", problems, tt.want)
		})
	}
}

type checkoutFile struct {
	Jobs map[string]struct {
		Steps []struct {
			Uses string         `yaml:"uses"`
			With map[string]any `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// checkCheckouts returns one message for each actions/checkout step that keeps the token in
// the git configuration. It also returns the number of checkout steps that it read. A
// workflow in exempt can keep the token.
func checkCheckouts(workflows map[string][]byte, exempt ...string) (problems []string, seen int) {
	for name, data := range workflows {
		var w checkoutFile
		if err := yaml.Unmarshal(data, &w); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for id, job := range w.Jobs {
			for _, step := range job.Steps {
				if !strings.HasPrefix(step.Uses, "actions/checkout@") {
					continue
				}
				seen++
				if slices.Contains(exempt, name) {
					continue
				}
				if v, ok := step.With["persist-credentials"]; !ok || v != false {
					problems = append(problems, fmt.Sprintf("%s: the checkout of job %s does not set persist-credentials: false", name, id))
				}
			}
		}
	}
	slices.Sort(problems)
	return problems, seen
}

// TestCheckoutsDropCredentials guards rule 2 of ADR 0040 section 8. Only release.yml keeps
// the token, because its job pushes the kit tag.
func TestCheckoutsDropCredentials(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	workflows := readDir(t, filepath.Join(root, ".github/workflows"), "*.yml")
	problems, seen := checkCheckouts(workflows, "release.yml")
	if seen == 0 {
		t.Fatal("no checkout step found: the test reads the wrong files")
	}
	if len(problems) != 0 {
		t.Fatalf("checkout steps keep the token:\n%s", strings.Join(problems, "\n"))
	}

	const keeps = "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@abc\n        with:\n          fetch-depth: 0\n"
	const drops = "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@abc\n        with:\n          persist-credentials: false\n"
	if p, _ := checkCheckouts(map[string][]byte{"x.yml": []byte(keeps)}); len(p) != 1 {
		t.Errorf("a checkout that keeps the token gave %v, want one problem", p)
	}
	if p, _ := checkCheckouts(map[string][]byte{"x.yml": []byte(keeps)}, "x.yml"); len(p) != 0 {
		t.Errorf("an exempt workflow gave %v, want no problem", p)
	}
	if p, _ := checkCheckouts(map[string][]byte{"x.yml": []byte(drops)}); len(p) != 0 {
		t.Errorf("a checkout that drops the token gave %v, want no problem", p)
	}
}
