package templates_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stopyellingatme/spinup/internal/ci"
	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"go.yaml.in/yaml/v3"
)

func TestGoldenCi(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "ci")
	for _, tc := range []struct {
		name, modules string
		push, web     bool
		files         int
	}{
		{"api-only", "ci", false, false, 3},
		{"full", "ci,deploy,backups", false, true, 6},
		{"deploy-on-push", "ci,deploy,backups", true, true, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answers := map[string]any{"modules": tc.modules, "app_name": "todo", "ci_deploy_on_push": tc.push}
			render := func() (fs.FS, error) { return renderCI(t, tc.web, answers), nil }
			tree, _ := render()
			testutil.Golden(t, "ci/"+tc.name, tree)
			testutil.Deterministic(t, render)
			names := checkCITree(t, tree)
			if len(names) != tc.files {
				t.Errorf("files = %v, want %d", names, tc.files)
			}
			data := readCIFile(t, tree, ".github/workflows/ci.yml")
			assertServicesCIWorkflow(t, data)
			if strings.Contains(data, "\n  web:\n") != tc.web {
				t.Error("wrong web job condition")
			}
			if strings.Contains(tc.modules, "deploy") {
				deploy := readCIFile(t, tree, ".github/workflows/deploy.yml")
				if strings.Contains(deploy, "\n  push:\n") != tc.push {
					t.Error("wrong deploy push trigger")
				}
			}
		})
	}
	for _, version := range []string{"18", "17", "16"} {
		t.Run("postgres-"+version, func(t *testing.T) {
			t.Parallel()
			tree := renderCI(t, true, map[string]any{"db_version": version, "modules": "ci", "app_name": "todo"})
			key := "postgres"
			if version != "18" {
				key += "-" + version
			}
			image, err := stackversions.MustLoad().Image(key)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"ci", "e2e"} {
				if !strings.Contains(readCIFile(t, tree, ".github/workflows/"+name+".yml"), "image: "+image.Ref()) {
					t.Errorf("%s lacks pinned image %s", name, image.Ref())
				}
			}
		})
	}
}

func assertServicesCIWorkflow(t *testing.T, data string) {
	t.Helper()
	for _, want := range []string{
		"\n  services:\n",
		"${{ fromJSON(needs.spinup.outputs.matrix) }}",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("CI workflow lacks %q", want)
		}
	}

	var document yaml.Node
	if err := yaml.Unmarshal([]byte(data), &document); err != nil {
		t.Fatal(err)
	}
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Value == "run" && strings.Contains(value.Value, "${{ matrix.") {
					t.Errorf("run block uses a matrix expression: %s", value.Value)
				}
			}
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&document)
}

// renderCI mirrors pack selection: web is independent of the optional modules.
func renderCI(t *testing.T, web bool, answers map[string]any) fs.FS {
	t.Helper()
	packs := []string{"ci"}
	if web {
		packs = append(packs, "base", "api", "web")
		answers["module"] = "example.com/todo"
	}
	all := testutil.RenderPack(t, packs, answers)
	selected := fstest.MapFS{}
	err := fs.WalkDir(all, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasPrefix(name, ".github/") {
			return err
		}
		data, err := fs.ReadFile(all, name)
		if err != nil {
			return err
		}
		selected[name] = &fstest.MapFile{Data: data, Mode: 0644}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return selected
}

func readCIFile(t *testing.T, tree fs.FS, name string) string {
	t.Helper()
	data, err := fs.ReadFile(tree, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func checkCITree(t *testing.T, tree fs.FS) []string {
	t.Helper()
	dir := t.TempDir()
	var names, workflows []string
	pinned := regexp.MustCompile(`@([a-f0-9]{40})$`)
	err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		names = append(names, name)
		data, err := fs.ReadFile(tree, name)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return err
		}
		var doc struct {
			Permissions map[string]string `yaml:"permissions"`
			Jobs        map[string]struct {
				Timeout int `yaml:"timeout-minutes"`
			} `yaml:"jobs"`
		}
		if strings.HasPrefix(name, ".github/workflows/") {
			workflows = append(workflows, target)
			if err := yaml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Permissions["contents"] != "read" || len(doc.Permissions) != 1 {
				t.Errorf("%s: top-level permissions = %v", name, doc.Permissions)
			}
			for id, job := range doc.Jobs {
				if job.Timeout <= 0 {
					t.Errorf("%s: %s lacks a timeout", name, id)
				}
			}
		}
		var node yaml.Node
		if err := yaml.Unmarshal(data, &node); err != nil {
			t.Fatal(err)
		}
		var visit func(*yaml.Node)
		visit = func(n *yaml.Node) {
			if n.Kind == yaml.MappingNode {
				for i := 0; i < len(n.Content); i += 2 {
					key, value := n.Content[i].Value, n.Content[i+1].Value
					if key == "uses" && !strings.HasPrefix(value, "./") && !pinned.MatchString(value) {
						t.Errorf("%s: unpinned action %s", name, value)
					}
					if key == "run" && n.Content[i+1].Kind == yaml.ScalarNode && !strings.HasPrefix(value, "set -euo pipefail\n") {
						t.Errorf("%s: run lacks strict mode: %s", name, value)
					}
				}
			}
			for _, child := range n.Content {
				visit(child)
			}
		}
		visit(&node)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spexec.LookPath("actionlint"); err != nil {
		t.Fatal("actionlint is required for CI golden tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := (spexec.OSRunner{}).Run(ctx, spexec.Cmd{Name: "actionlint", Args: append([]string{"-color"}, workflows...), Dir: dir})
	if err != nil {
		t.Fatalf("actionlint: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	return names
}

func TestCIPackOwnership(t *testing.T) {
	t.Parallel()
	pack, err := templates.Load(templates.Embedded(), "ci")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range pack.Files {
		names = append(names, file.Path)
		if file.Class != "managed" {
			t.Errorf("%s: class = %s", file.Path, file.Class)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, ci.ManagedFiles()) {
		t.Errorf("manifest = %v, helper = %v", names, ci.ManagedFiles())
	}
}

func TestCIWorkflowRegressions(t *testing.T) {
	t.Parallel()
	tree := renderCI(t, true, map[string]any{"modules": "ci,deploy,backups", "app_name": "todo", "ci_deploy_on_push": true})
	nodeMajor, _, _ := strings.Cut(stackversions.MustLoad().Images["node"].Tag, "-")
	for _, name := range []string{"ci", "e2e", "upgrade-check"} {
		t.Run(name+"-node", func(t *testing.T) {
			t.Parallel()
			var workflow struct {
				Jobs map[string]struct {
					Steps []struct {
						Uses string            `yaml:"uses"`
						With map[string]string `yaml:"with"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal([]byte(readCIFile(t, tree, ".github/workflows/"+name+".yml")), &workflow); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if strings.HasPrefix(step.Uses, "actions/setup-node@") {
						count++
						if step.With["node-version"] != nodeMajor || step.With["node-version-file"] != "" {
							t.Errorf("setup-node options = %v, want Node %s", step.With, nodeMajor)
						}
					}
				}
			}
			if count == 0 {
				t.Fatal("no setup-node step checked")
			}
		})
	}
	for _, tc := range []struct {
		name, expression string
	}{
		{"backup-verify", "${{ vars.SPINUP_ENVIRONMENT || 'staging' }}"},
		{"deploy", "${{ inputs.environment || vars.SPINUP_ENVIRONMENT || 'staging' }}"},
	} {
		t.Run(tc.name+"-environment", func(t *testing.T) {
			t.Parallel()
			data := readCIFile(t, tree, ".github/workflows/"+tc.name+".yml")
			for _, prefix := range []string{"environment: ", "SPINUP_ENVIRONMENT: ", "group: spinup-remote-"} {
				if !strings.Contains(data, prefix+tc.expression) {
					t.Errorf("missing %s%s", prefix, tc.expression)
				}
			}
		})
	}
	upgrade := readCIFile(t, tree, ".github/workflows/upgrade-check.yml")
	if got := strings.Count(upgrade, "token: ${{ secrets.SPINUP_UPGRADE_TOKEN || github.token }}"); got != 2 {
		t.Errorf("upgrade token used %d times, want checkout and create-pull-request", got)
	}
	for _, want := range []string{"SPINUP_UPGRADE_TOKEN: ${{ secrets.SPINUP_UPGRADE_TOKEN }}", `if [ -z "${SPINUP_UPGRADE_TOKEN:-}" ]; then`, "cannot push workflow changes", "does not trigger pull_request workflows"} {
		if !strings.Contains(upgrade, want) {
			t.Errorf("upgrade PR body lacks fallback guidance: %s", want)
		}
	}
	if data := readCIFile(t, tree, ".github/workflows/ci.yml"); strings.Contains(data, "--exclude") {
		t.Error("CI must use the shared Vitest exclusion")
	}
}

func TestGoldenCIWebScaffold(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "ci")
	for _, auth := range []bool{false, true} {
		name := "no-auth"
		if auth {
			name = "auth"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			packs := []string{"base", "api", "web"}
			modules := "ci"
			if auth {
				packs = append(packs, "auth")
				modules += ",auth"
			}
			tree := testutil.RenderPack(t, packs, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": modules})
			selected := fstest.MapFS{}
			for _, path := range []string{"web/e2e/smoke.spec.ts", "web/vite.config.js"} {
				selected[path] = &fstest.MapFile{Data: []byte(readCIFile(t, tree, path)), Mode: 0644}
			}
			testutil.Golden(t, "ci/web-scaffold/"+name, selected)
			config := readCIFile(t, tree, "web/vite.config.js")
			for _, want := range []string{"import { configDefaults } from 'vitest/config'", "exclude: ['e2e/**', ...configDefaults.exclude]"} {
				if !strings.Contains(config, want) {
					t.Errorf("Vitest config lacks %s", want)
				}
			}
			if auth {
				smoke := readCIFile(t, tree, "web/e2e/smoke.spec.ts")
				for _, tc := range []struct {
					locator string
					count   int
				}{
					{"banner.getByRole('link', { name: 'Sign in', exact: true })", 1},
					{"page.getByRole('menuitem', { name: 'Sign out', exact: true })", 1},
					{"banner.getByTestId('user-email')", 3},
					{"main.getByLabel('Email', { exact: true })", 2},
					{"main.getByLabel('Password', { exact: true })", 2},
					{"main.getByRole('button', { name: 'Create account', exact: true })", 1},
					{"main.getByRole('button', { name: 'Sign in', exact: true })", 1},
				} {
					if got := strings.Count(smoke, tc.locator); got != tc.count {
						t.Errorf("%s occurs %d times, want %d", tc.locator, got, tc.count)
					}
				}
			}
		})
	}
}
