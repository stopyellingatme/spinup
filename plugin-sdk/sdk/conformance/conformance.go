// Package conformance tests executable plugins against the API 1 protocol.
// Tests run binaries with full user privileges. They do not provide a sandbox.
package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

// Options supplies valid sample inputs for plugins with constrained arguments.
type Options struct {
	// Args overrides the default argument "sample" for each generator.
	Args map[string][]string
	// Answers supplies project answers to every plan call.
	Answers map[string]any
	// Timeout bounds each process. The default is five seconds, up to sixty seconds.
	Timeout time.Duration
}

// Run checks the protocol, deterministic plans, path safety, and tree integrity.
// Each positional argument defaults to "sample". Use RunWithOptions for other inputs.
func Run(t *testing.T, binary string) { t.Helper(); RunWithOptions(t, binary, Options{}) }

// RunWithOptions checks a plugin with explicit sample arguments and answers.
func RunWithOptions(t *testing.T, binary string, options Options) {
	t.Helper()
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "work")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conformance.txt"), []byte("Preserve this file.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(binary, sandbox, root, options); err != nil {
		t.Fatal(err)
	}
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var argumentName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
var version = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

type suite struct {
	binary, sandbox, root string
	timeout               time.Duration
}

func run(binary, sandbox, root string, options Options) error {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}
	s := suite{binary, sandbox, root, timeout}
	helloRaw, err := s.call("handshake", nil)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	hello, err := handshake(helloRaw)
	if err != nil {
		return err
	}
	raw, err := s.call("describe", nil)
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	if err := required(raw, "generators", "checks"); err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	var description sdk.DescribeResult
	if err := json.Unmarshal(raw, &description); err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	if err := describe(hello, description); err != nil {
		return err
	}
	for _, generator := range description.Generators {
		args, ok := options.Args[generator.Name]
		if !ok {
			args = make([]string, len(generator.Args))
			for i := range args {
				args[i] = "sample"
			}
		}
		params := sdk.PlanParams{Generator: generator.Name, Args: args, Answers: options.Answers}
		first, err := s.plan(params, false)
		if err != nil {
			return fmt.Errorf("plan %s: %w", generator.Name, err)
		}
		second, err := s.plan(params, false)
		if err != nil {
			return fmt.Errorf("plan %s: %w", generator.Name, err)
		}
		if !bytes.Equal(first, second) {
			return fmt.Errorf("plan %s determinism: result bytes differ", generator.Name)
		}
		// Replace each argument independently, preserving the other valid arguments.
		probes := append(TraversalArguments(), HugeArgument())
		for i := range args {
			for _, probe := range probes {
				malicious := params
				malicious.Args = slices.Clone(args)
				malicious.Args[i] = probe
				if _, err := s.plan(malicious, true); err != nil {
					return fmt.Errorf("malicious plan %s argument %d: %w", generator.Name, i, err)
				}
			}
		}
	}
	for _, check := range description.Checks {
		raw, err := s.call("check", sdk.CheckParams{Check: check.Name})
		if err != nil {
			return fmt.Errorf("check %s: %w", check.Name, err)
		}
		if err := checkShape(raw); err != nil {
			return err
		}
	}
	if err := s.expectError("unknown method", request("spinup.conformance.unknown", nil), sdk.MethodNotFound, "2"); err != nil {
		return err
	}
	for _, probe := range []struct {
		name, raw, id string
		code          int
	}{
		{"malformed JSON", "{", "null", sdk.ParseError},
		{"malformed version", `{"jsonrpc":"1.0","id":2,"method":"describe"}`, "2", sdk.InvalidRequest},
		{"malformed ID", `{"jsonrpc":"2.0","id":{},"method":"describe"}`, "null", sdk.InvalidRequest},
		{"malformed params", `{"jsonrpc":"2.0","id":2,"method":"plan","params":[]}`, "2", sdk.InvalidParams},
	} {
		if err := s.expectError(probe.name, []byte(probe.raw), probe.code, probe.id); err != nil {
			return err
		}
	}
	return s.timeoutProbe()
}

func handshake(raw []byte) (sdk.HandshakeResult, error) {
	var hello sdk.HandshakeResult
	if err := required(raw, "name", "version", "api", "capabilities"); err != nil {
		return hello, fmt.Errorf("handshake: %w", err)
	}
	if err := json.Unmarshal(raw, &hello); err != nil {
		return hello, fmt.Errorf("handshake: %w", err)
	}
	if !identifier.MatchString(hello.Name) || !version.MatchString(hello.Version) || hello.API != sdk.API {
		return hello, errors.New("handshake: invalid name, version, or API")
	}
	seen := map[string]bool{}
	for _, capability := range hello.Capabilities {
		if seen[capability] || capability != sdk.CapabilityGenerator && capability != sdk.CapabilityCheck {
			return hello, fmt.Errorf("handshake: invalid capability %q", capability)
		}
		seen[capability] = true
	}
	return hello, nil
}

func describe(hello sdk.HandshakeResult, d sdk.DescribeResult) error {
	if len(d.Generators) > 0 && !slices.Contains(hello.Capabilities, sdk.CapabilityGenerator) || len(d.Checks) > 0 && !slices.Contains(hello.Capabilities, sdk.CapabilityCheck) {
		return errors.New("describe: missing capability")
	}
	seen := map[string]bool{}
	for _, g := range d.Generators {
		if !identifier.MatchString(g.Name) || seen[g.Name] || strings.TrimSpace(g.Short) == "" || strings.TrimSpace(g.Usage) == "" {
			return fmt.Errorf("describe: invalid or duplicate generator %q", g.Name)
		}
		seen[g.Name] = true
		args := map[string]bool{}
		for _, arg := range g.Args {
			if !argumentName.MatchString(arg) || args[arg] {
				return fmt.Errorf("describe: invalid argument %q", arg)
			}
			args[arg] = true
		}
	}
	seen = map[string]bool{}
	for _, c := range d.Checks {
		if !identifier.MatchString(c.Name) || seen[c.Name] || strings.TrimSpace(c.Title) == "" {
			return fmt.Errorf("describe: invalid or duplicate check %q", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

func checkShape(raw []byte) error {
	if err := required(raw, "findings"); err != nil {
		return fmt.Errorf("check: %w", err)
	}
	var result sdk.CheckResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("check: %w", err)
	}
	for _, f := range result.Findings {
		if !slices.Contains([]string{"ok", "warn", "fail", "skip"}, f.Status) || strings.TrimSpace(f.Message) == "" {
			return errors.New("check: invalid finding")
		}
	}
	return nil
}

func (s suite) plan(params sdk.PlanParams, allowError bool) ([]byte, error) {
	raw, err := s.call("plan", params)
	if err != nil {
		var protocolError *sdk.Error
		if allowError && errors.As(err, &protocolError) {
			return nil, nil
		}
		return nil, err
	}
	if err := required(raw, "files"); err != nil {
		return nil, err
	}
	var result sdk.PlanResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if len(result.Next) > 2 {
		return nil, errors.New("plan: more than two next commands")
	}
	seen := map[string]bool{}
	for _, f := range result.Files {
		if err := safePath(f.Path); err != nil {
			return nil, err
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return nil, fmt.Errorf("plan: duplicate path %q", f.Path)
		}
		seen[key] = true
		if !slices.Contains([]string{"managed", "scaffold", "seed"}, f.Class) {
			return nil, fmt.Errorf("plan: invalid class %q", f.Class)
		}
		if f.Mode & ^uint32(0777) != 0 {
			return nil, fmt.Errorf("plan: invalid mode %o", f.Mode)
		}
	}
	return raw, nil
}

func required(raw []byte, keys ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("result is missing %s", key)
		}
	}
	return nil
}

// TraversalArguments returns independent path traversal and protected-path probes.
func TraversalArguments() []string {
	return []string{"../escape", "../../escape", "/tmp/spinup-conformance-escape", `..\escape`, `C:\escape`, ".git/config", ".spinup/plugins.allow", ".github/workflows/escape.yml", "spinup.toml", "spinup.lock", "name\x00suffix"}
}

// HugeArgument returns a one MiB argument below the protocol message limit.
func HugeArgument() string { return strings.Repeat("x", 1<<20) }
