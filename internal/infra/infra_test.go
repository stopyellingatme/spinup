package infra

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
)

const config = `# Keep this comment.
modules = ["tofu", "deploy"]
[app]
name = "todo"
module = "example.com/todo"
[env.staging]
host = "old.example.com" # Keep this host comment.
user = "deploy"
[env.production]
host = "production.example.com"
protected = true
`

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(config), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deploy/infra/tofu"), 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func deps(fake *runexec.Fake) Deps {
	return Deps{Runner: fake, LookPath: func(string) (string, error) { return "tofu", nil }, Getenv: func(key string) string {
		if key == "HCLOUD_TOKEN" {
			return "secret-token"
		}
		return ""
	}}
}

func TestRun(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"plan", "apply", "output", "destroy"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			root := fixture(t)
			var out, errOut bytes.Buffer
			fake := &runexec.Fake{}
			fake.Expect("tofu", "-chdir=deploy/infra/tofu", "init", "-input=false", "-backend-config=path=terraform.staging.tfstate").Return("initialized\n", "", 0)
			args := []string{"-chdir=deploy/infra/tofu", action}
			if action != "output" {
				args = append(args, "-input=false")
			}
			if action == "apply" || action == "destroy" {
				args = append(args, "-auto-approve")
			}
			fake.Expect("tofu", args...).Return("result\n", "warning\n", 0)
			if action == "apply" {
				fake.Expect("tofu", "-chdir=deploy/infra/tofu", "output", "-raw", "ipv4").Return("192.0.2.5\n", "", 0)
			}
			err := Run(t.Context(), Options{Dir: root, Env: "staging", Action: action, IKnow: true, Confirm: project.Confirm{Yes: true}, Out: &out, ErrOut: &errOut}, deps(fake))
			if err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			if !strings.Contains(out.String(), "result\n") || !strings.Contains(errOut.String(), "warning\n") {
				t.Fatalf("streams = %q, %q", &out, &errOut)
			}
			for _, call := range fake.Calls() {
				if call.Dir != root {
					t.Errorf("command directory = %q", call.Dir)
				}
				joined := strings.Join(call.Env, "\n")
				if !strings.Contains(joined, "HCLOUD_TOKEN=secret-token") || !strings.Contains(joined, "TF_DATA_DIR="+filepath.Join(root, "deploy/infra/tofu/.terraform/staging")) {
					t.Errorf("command environment = %v", call.Env)
				}
				if len(call.Redact) != 1 || call.Redact[0] != "secret-token" {
					t.Error("token is not redacted")
				}
			}
			got, err := os.ReadFile(filepath.Join(root, "spinup.toml"))
			if err != nil {
				t.Fatal(err)
			}
			want := config
			if action == "apply" {
				want = strings.Replace(config, `"old.example.com"`, `'192.0.2.5'`, 1)
			}
			if string(got) != want {
				t.Fatalf("config = %s\nwant = %s", got, want)
			}
			info, err := os.Stat(filepath.Join(root, "spinup.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0640 {
				t.Errorf("config mode = %o", info.Mode().Perm())
			}
		})
	}
}

func TestRunGuards(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, action, env, want string
		know                    bool
		change                  func(*Deps)
	}{
		{name: "missing token", action: "plan", env: "staging", want: "HCLOUD_TOKEN", change: func(d *Deps) { d.Getenv = func(string) string { return "" } }},
		{name: "missing binary", action: "plan", env: "staging", want: "spinup tools install", change: func(d *Deps) { d.LookPath = func(string) (string, error) { return "", errors.New("missing") } }},
		{name: "missing env", action: "plan", want: "--env"},
		{name: "unknown env", action: "plan", env: "missing", want: "--env"},
		{name: "invalid action", action: "bad", env: "staging", want: "action"},
		{name: "destroy flag", action: "destroy", env: "staging", want: "--i-know"},
		{name: "protected", action: "destroy", env: "production", know: true, want: "protected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &runexec.Fake{}
			d := deps(fake)
			if tc.change != nil {
				tc.change(&d)
			}
			err := Run(t.Context(), Options{Dir: fixture(t), Action: tc.action, Env: tc.env, IKnow: tc.know}, d)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error = %v", err)
			}
			if len(fake.Calls()) != 0 {
				t.Fatal("guard ran tofu")
			}
		})
	}
}

func TestApplyFailuresPreserveConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, output                    string
		initFail, applyFail, outputFail bool
	}{
		{name: "init fails", initFail: true}, {name: "apply fails", applyFail: true}, {name: "output fails", outputFail: true},
		{name: "empty output"}, {name: "invalid output", output: "host.example.com"}, {name: "IPv6 output", output: "2001:db8::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := fixture(t)
			fake := &runexec.Fake{}
			init := fake.Expect("tofu", "-chdir=deploy/infra/tofu", "init")
			if tc.initFail {
				init.Fail(errors.New("init failed"))
			} else {
				apply := fake.Expect("tofu", "-chdir=deploy/infra/tofu", "apply")
				if tc.applyFail {
					apply.Fail(errors.New("apply failed"))
				} else {
					output := fake.Expect("tofu", "-chdir=deploy/infra/tofu", "output", "-raw", "ipv4")
					if tc.outputFail {
						output.Fail(errors.New("output failed"))
					} else {
						output.Return(tc.output, "", 0)
					}
				}
			}
			err := Run(t.Context(), Options{Dir: root, Env: "staging", Action: "apply", Confirm: project.Confirm{Yes: true}}, deps(fake))
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error = %v", err)
			}
			fake.AssertDone(t)
			got, err := os.ReadFile(filepath.Join(root, "spinup.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != config {
				t.Fatalf("config changed: %s", got)
			}
		})
	}
}

func TestInitializedEnvironment(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	dataDir := filepath.Join(root, "deploy/infra/tofu/.terraform/staging")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "terraform.tfstate"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &runexec.Fake{}
	fake.Expect("tofu", "-chdir=deploy/infra/tofu", "output").Return("192.0.2.5", "", 0)
	if err := Run(t.Context(), Options{Dir: root, Env: "staging", Action: "output"}, deps(fake)); err != nil {
		t.Fatal(err)
	}
	fake.AssertDone(t)
}

func TestProtectedDestroy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input          string
		interactive, yes, ci bool
		want                 string
	}{
		{name: "typed confirmation", input: "production\n", interactive: true},
		{name: "CI approval", yes: true, ci: true},
		{name: "wrong confirmation", input: "wrong\n", interactive: true, want: "confirmation"},
		{name: "yes outside CI", yes: true, want: "protected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &runexec.Fake{}
			if tc.want == "" {
				fake.Expect("tofu", "-chdir=deploy/infra/tofu", "init")
				fake.Expect("tofu", "-chdir=deploy/infra/tofu", "destroy")
			}
			var prompt bytes.Buffer
			err := Run(t.Context(), Options{Dir: fixture(t), Env: "production", Action: "destroy", IKnow: true, Confirm: project.Confirm{In: strings.NewReader(tc.input), Out: &prompt, Interactive: tc.interactive, Yes: tc.yes, CI: tc.ci}}, deps(fake))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v", err)
				}
				if len(fake.Calls()) != 0 {
					t.Fatal("ran tofu after rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			if tc.interactive && !strings.Contains(prompt.String(), "production") {
				t.Fatal("no protected environment prompt")
			}
			call := fake.Calls()[1]
			if tc.interactive && strings.Contains(strings.Join(call.Args, " "), "-input=false") {
				t.Fatal("disabled interactive approval")
			}
		})
	}
}

func TestDryRunAndJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		action    string
		dry, json bool
		args      []string
	}{
		{action: "apply", dry: true, args: []string{"plan", "-input=false"}},
		{action: "destroy", dry: true, args: []string{"plan", "-input=false", "-destroy"}},
		{action: "output", json: true, args: []string{"output", "-json"}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			t.Parallel()
			root := fixture(t)
			fake := &runexec.Fake{}
			fake.Expect("tofu", "-chdir=deploy/infra/tofu", "init")
			fake.Expect("tofu", append([]string{"-chdir=deploy/infra/tofu"}, tc.args...)...)
			if err := Run(t.Context(), Options{Dir: root, Env: "staging", Action: tc.action, DryRun: tc.dry, JSON: tc.json}, deps(fake)); err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			data, err := os.ReadFile(filepath.Join(root, "spinup.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != config {
				t.Fatal("dry run changed config")
			}
		})
	}
}
