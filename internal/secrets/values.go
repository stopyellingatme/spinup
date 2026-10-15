package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// Decrypt returns the flat secret map for the deployer. It never logs values.
func Decrypt(ctx context.Context, root, env string) (map[string]string, error) {
	e, err := resolve(Options{Dir: root, Env: env})
	if err != nil {
		return nil, err
	}
	return e.decrypt(ctx, runexec.OSRunner{}, e.path)
}

func (e environment) decrypt(ctx context.Context, r runexec.Runner, path string) (map[string]string, error) {
	dir, err := os.MkdirTemp("", "spinup-secrets-*")
	if err != nil {
		return nil, problem("decrypt secrets", "a private temporary directory could not be created", "check the temporary directory permissions")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	output := filepath.Join(dir, "values.json")
	// Precreate the destination with restrictive permissions, even if sops uses
	// the process umask for an existing output file.
	if err = os.WriteFile(output, nil, 0600); err != nil {
		return nil, problem("decrypt secrets", "a private output file could not be created", "check the temporary directory permissions")
	}
	c := e.command("decrypt", "--output-type", "json", "--output", output, path)
	if err = runTool(ctx, r, c); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return nil, problem("decrypt secrets", "the decrypted output could not be read", "check the temporary directory permissions")
	}
	return parseValues(data)
}

func parseValues(data []byte) (map[string]string, error) {
	bad := func() (map[string]string, error) {
		return nil, problem("decode secrets", "expected one flat map with unique names and string entries", "use uppercase names and quote each YAML string")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return bad()
	}
	values := map[string]string{}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !namePattern.MatchString(name) {
			return bad()
		}
		if _, exists := values[name]; exists {
			return bad()
		}
		// Decode a token to reject null as well as arrays, numbers, and objects.
		token, err = decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return bad()
		}
		values[name] = value
	}
	if _, err = decoder.Token(); err != nil {
		return bad()
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return bad()
	}
	return values, nil
}

func encodeString(value string) string { data, _ := json.Marshal(value); return string(data) }

func redactions(values map[string]string) []string {
	result := make([]string, 0, len(values)*2)
	for _, name := range sortedNames(values) {
		result = append(result, values[name], encodeString(values[name]))
	}
	return result
}

func sortedNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Diff prints only names added, removed, or changed since Git HEAD.
// An environment without a committed secrets file compares against an empty map.
func Diff(ctx context.Context, o Options, r runexec.Runner) error {
	e, err := resolve(o)
	if err != nil {
		return err
	}
	current, err := e.decrypt(ctx, r, e.path)
	if err != nil {
		return err
	}
	git := func(args ...string) (runexec.Result, error) {
		return r.Run(ctx, runexec.Cmd{Name: "git", Args: args, Dir: e.root})
	}
	if _, err = git("rev-parse", "--is-inside-work-tree"); err != nil {
		return problem("compare secrets", "the project is not an accessible Git repository", "initialize Git in the project")
	}
	previous := map[string]string{}
	result, err := git("rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil && result.ExitCode != 1 {
		return problem("compare secrets", "Git HEAD could not be inspected", "check the Git repository")
	}
	if err == nil {
		result, err = git("ls-tree", "--name-only", "HEAD", "--", filepath.ToSlash(e.path))
		if err != nil {
			return problem("compare secrets", "the committed file could not be inspected", "check the Git repository")
		}
		if strings.TrimSpace(string(result.Stdout)) != "" {
			result, err = git("show", "HEAD:"+filepath.ToSlash(e.path))
			if err != nil {
				return problem("compare secrets", "the committed file could not be read", "check the Git repository")
			}
			dir, err := os.MkdirTemp("", "spinup-secrets-diff-*")
			if err != nil {
				return problem("compare secrets", "a temporary directory could not be created", "check temporary directory permissions")
			}
			defer func() { _ = os.RemoveAll(dir) }()
			path := filepath.Join(dir, "previous.enc.yaml")
			if err = os.WriteFile(path, result.Stdout, 0600); err != nil {
				return problem("compare secrets", "the committed ciphertext could not be saved", "check temporary directory permissions")
			}
			previous, err = e.decrypt(ctx, r, path)
			if err != nil {
				return err
			}
		}
	}
	return writeDiff(o.Out, previous, current)
}

func writeDiff(out io.Writer, previous, current map[string]string) error {
	if out == nil {
		return problem("compare secrets", "stdout is unavailable", "provide an output stream")
	}
	names := map[string]string{}
	for name := range previous {
		names[name] = ""
	}
	for name := range current {
		names[name] = ""
	}
	for _, name := range sortedNames(names) {
		old, was := previous[name]
		value, is := current[name]
		change := "changed"
		switch {
		case !was:
			change = "added"
		case !is:
			change = "removed"
		case old == value:
			continue
		}
		if _, err := io.WriteString(out, change+" "+name+"\n"); err != nil {
			return problem("compare secrets", "stdout could not be written", "check the output stream")
		}
	}
	return nil
}
