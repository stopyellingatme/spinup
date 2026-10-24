package secrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestCreationRule(t *testing.T) {
	t.Parallel()
	source := []byte("# Keep this header.\ncreation_rules: [] # Keep this comment.\n")
	out, err := creationRule(source, "staging", []string{"age1env", "age1recver", "age1ci"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Keep this header.", "# Keep this comment.", `^secrets/staging\.enc\.yaml$`, "age1env,age1recver,age1ci"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	again, err := creationRule(out, "staging", []string{"age1env", "age1new"})
	if err != nil || bytes.Count(again, []byte("path_regex:")) != 1 || !bytes.Contains(again, []byte("age1env,age1new")) {
		t.Fatalf("update: %s, %v", again, err)
	}
	final, err := creationRule(again, "production", []string{"age1prod"})
	if err != nil || bytes.Count(final, []byte("path_regex:")) != 2 {
		t.Fatalf("append: %s, %v", final, err)
	}
}

func TestInitGuards(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, env, recovery string
		noRecovery          bool
	}{
		{name: "recovery required", env: "staging"},
		{name: "environment required", recovery: "age1key"},
		{name: "environment traversal", env: "../staging", recovery: "age1key"},
		{name: "bad recipient", env: "staging", recovery: "not-age"},
		{name: "conflicting recovery", env: "staging", recovery: "age1key", noRecovery: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &runexec.Fake{}
			err := Init(context.Background(), Options{Dir: fixture(t), Env: tt.env, RecoveryRecipient: tt.recovery, NoRecovery: tt.noRecovery}, f)
			if err == nil || !strings.Contains(err.Error(), "Try:") || len(f.Calls()) != 0 {
				t.Fatalf("guard = %v, calls=%v", err, f.Calls())
			}
		})
	}
}

func TestProtectedAndEditGuards(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	f := &runexec.Fake{}
	for _, operation := range []func(context.Context, Options, runexec.Runner) error{Rotate, Rekey, Edit} {
		err := operation(context.Background(), Options{Dir: root, Env: "production"}, f)
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("guard = %v", err)
		}
	}
	if len(f.Calls()) != 0 {
		t.Fatal("guard ran a command")
	}
}

func TestConfirmNameAnswersProtectedGuard(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	f := &runexec.Fake{}
	for _, operation := range []func(context.Context, Options, runexec.Runner) error{Rotate, Rekey} {
		err := operation(context.Background(), Options{Dir: root, Env: "production", ConfirmName: "staging"}, f)
		if err == nil || !strings.Contains(err.Error(), "confirm secrets change") {
			t.Fatalf("wrong name = %v", err)
		}
		err = operation(context.Background(), Options{Dir: root, Env: "production", ConfirmName: "production", DryRun: true}, f)
		if err != nil && strings.Contains(err.Error(), "confirm secrets change") {
			t.Fatalf("right name = %v", err)
		}
	}
}

func TestParseValues(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`{"NAME":"value"}`, `{}`, `{"NAME":1}`, `{"NAME":{"nested":"value"}}`, `{"../bad":"value"}`, `{"NAME":"value","NAME":"other"}`, `{"NAME":"value"} {}`} {
		_, err := parseValues([]byte(data))
		want := data == `{"NAME":"value"}` || data == `{}`
		if (err == nil) != want {
			t.Fatalf("parse %s = %v", data, err)
		}
		if err != nil && strings.Contains(err.Error(), "value") {
			t.Fatalf("parse exposed content: %v", err)
		}
	}
}

func TestDiffNames(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeDiff(&out, map[string]string{"OLD": "sensitive", "SAME": "same", "CHANGED": "before"}, map[string]string{"NEW": "secret", "SAME": "same", "CHANGED": "after"})
	if err != nil || out.String() != "changed CHANGED\nadded NEW\nremoved OLD\n" {
		t.Fatalf("diff=%q, %v", out.String(), err)
	}
}

func TestToolErrorDoesNotLeak(t *testing.T) {
	t.Parallel()
	f := &runexec.Fake{}
	f.Expect("sops").Fail(errors.New("tool error includes secret-value"))
	err := runTool(context.Background(), f, runexec.Cmd{Name: "sops", Redact: []string{"secret-value"}})
	if err == nil || strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error=%v", err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	data := "[app]\nname = 'demo'\nmodule = 'example.com/demo'\n[env.production]\nprotected = true\n"
	if err := os.WriteFile(filepath.Join(root, project.ConfigFile), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

type fileRunner struct {
	t      *testing.T
	fake   runexec.Fake
	values string
}

func (r *fileRunner) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	r.t.Helper()
	if c.Name == "age-keygen" && len(c.Args) > 1 && c.Args[0] == "-o" {
		if err := os.WriteFile(c.Args[1], []byte("private test key"), 0666); err != nil {
			r.t.Fatal(err)
		}
	}
	if c.Name == "sops" && len(c.Args) > 4 && c.Args[0] == "decrypt" {
		if err := os.WriteFile(c.Args[4], []byte(r.values), 0600); err != nil {
			r.t.Fatal(err)
		}
	}
	return r.fake.Run(ctx, c)
}

func TestSetAndGet(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	if err := os.Mkdir(filepath.Join(root, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets/staging.enc.yaml"), []byte("ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	r := &fileRunner{t: t, values: `{"OTHER":"old-private","NAME":"before"}`}
	r.fake.Expect("sops", "decrypt").Return("", "", 0)
	r.fake.Expect("sops", "set", "--value-stdin").Return("", "", 0)
	value := "new-private\n"
	if err := Set(context.Background(), Options{Dir: root, Env: "staging", In: strings.NewReader(value)}, "NAME", r); err != nil {
		t.Fatal(err)
	}
	calls := r.fake.Calls()
	c := calls[1]
	input, err := io.ReadAll(c.Stdin)
	if err != nil || string(input) != `"new-private\n"` || strings.Contains(c.String(), "new-private") {
		t.Fatalf("command input=%q err=%v", input, err)
	}
	if !slices.Contains(c.Redact, value) || !slices.Contains(c.Redact, "old-private") || !slices.Contains(c.Redact, "before") {
		t.Fatal("missing redactions")
	}
	if c.Stdout != nil || c.Stderr != nil {
		t.Fatal("tool output was exposed")
	}
	r.fake.Expect("sops", "decrypt").Return("", "", 0)
	var out bytes.Buffer
	if err := Get(context.Background(), Options{Dir: root, Env: "staging", Out: &out}, "NAME", r); err != nil || out.String() != "before" {
		t.Fatalf("get output=%q err=%v", out.String(), err)
	}
	r.fake.AssertDone(t)
}

func TestInitPermissionsAndRecovery(t *testing.T) {
	t.Setenv("SPINUP_KEYS_DIR", filepath.Join(t.TempDir(), "keys"))
	root := fixture(t)
	r := &fileRunner{t: t}
	r.fake.Expect("age-keygen", "-o").Return("", "", 0)
	r.fake.Expect("age-keygen", "-y").Return("age1test\n", "", 0)
	r.fake.Expect("sops", "encrypt").Return("sops:\n  age: []\n", "", 0)
	o := Options{Dir: root, Env: "staging", RecoveryRecipient: "age1recver", CIRecipient: "age1cprec"}
	if err := Init(context.Background(), o, r); err != nil {
		t.Fatal(err)
	}
	key, err := KeyPath("demo", "staging")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path string
		mode os.FileMode
	}{{key, 0600}, {filepath.Dir(key), 0700}, {filepath.Join(root, "secrets/staging.enc.yaml"), 0600}} {
		info, err := os.Stat(tt.path)
		if err != nil || info.Mode().Perm() != tt.mode {
			t.Fatalf("permissions for %s: %v, %v", tt.path, info, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(root, ".sops.yaml"))
	if err != nil || !bytes.Contains(config, []byte("age1test,age1recver,age1cprec")) {
		t.Fatalf("config=%s err=%v", config, err)
	}
	r.fake.AssertDone(t)
}

func TestInitFailureRestoresConfiguration(t *testing.T) {
	t.Setenv("SPINUP_KEYS_DIR", filepath.Join(t.TempDir(), "keys"))
	root := fixture(t)
	source := []byte("# Original.\ncreation_rules: []\n")
	if err := os.WriteFile(filepath.Join(root, ".sops.yaml"), source, 0644); err != nil {
		t.Fatal(err)
	}
	r := &fileRunner{t: t}
	r.fake.Expect("age-keygen", "-o").Return("", "", 0)
	r.fake.Expect("age-keygen", "-y").Return("age1test\n", "", 0)
	r.fake.Expect("sops", "encrypt").Return("", "failure", 1)
	if err := Init(context.Background(), Options{Dir: root, Env: "staging", RecoveryRecipient: "age1recver"}, r); err == nil {
		t.Fatal("accepted encryption failure")
	}
	actual, err := os.ReadFile(filepath.Join(root, ".sops.yaml"))
	if err != nil || !bytes.Equal(actual, source) {
		t.Fatalf("rollback=%s %v", actual, err)
	}
	key, err := KeyPath("demo", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(key); err != nil {
		t.Fatal("lost generated key")
	}
	r.fake.AssertDone(t)
}

func TestNoRecoveryWarning(t *testing.T) {
	t.Setenv("SPINUP_KEYS_DIR", filepath.Join(t.TempDir(), "keys"))
	r := &fileRunner{t: t}
	r.fake.Expect("age-keygen", "-o").Return("", "", 0)
	r.fake.Expect("age-keygen", "-y").Return("age1test\n", "", 0)
	r.fake.Expect("sops", "encrypt").Return("sops:\n  age: []\n", "", 0)
	var warning bytes.Buffer
	if err := Init(context.Background(), Options{Dir: fixture(t), Env: "staging", NoRecovery: true, ErrOut: &warning}, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning.String(), "WARNING") || !strings.Contains(warning.String(), "permanently") {
		t.Fatalf("warning=%q", warning.String())
	}
	r.fake.AssertDone(t)
}
