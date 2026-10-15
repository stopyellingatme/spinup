package plainsecrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"go.yaml.in/yaml/v3"
)

func init() {
	doctor.Register(doctor.Check{ID: "plaintext-secrets", Title: "Plaintext secrets", Scope: doctor.Local, Run: runCheck})
}

func runCheck(ctx context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" || c.Quick {
		return doctor.Result{Status: doctor.Skip, Message: "requires a project and a full check"}
	}
	r, ok := c.Deps["runner"].(runexec.Runner)
	if !ok {
		r = runexec.OSRunner{}
	}
	result, err := r.Run(ctx, runexec.Cmd{Name: "git", Args: []string{"ls-files", "-z"}, Dir: c.Root})
	if err != nil {
		return failure("tracked files could not be inspected")
	}
	var problems []string
	for _, name := range strings.Split(string(result.Stdout), "\x00") {
		if name == "" {
			continue
		}
		if !fsPath(name) {
			return failure("Git returned an unsafe path")
		}
		switch {
		case path.Base(name) == ".env" || strings.HasSuffix(name, ".agekey"):
			problems = append(problems, "tracked private file: "+name)
		case strings.HasPrefix(name, "secrets/") && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")):
			file := filepath.Join(c.Root, filepath.FromSlash(name))
			info, err := os.Lstat(file)
			if err != nil || !info.Mode().IsRegular() {
				problems = append(problems, "unreadable secrets file: "+name)
				continue
			}
			data, err := os.ReadFile(file)
			if err != nil || !encrypted(data) {
				problems = append(problems, "tracked plaintext secrets: "+name)
				continue
			}
			staged, err := r.Run(ctx, runexec.Cmd{Name: "git", Args: []string{"show", ":" + name}, Dir: c.Root})
			if err != nil || !encrypted(staged.Stdout) {
				problems = append(problems, "staged plaintext secrets: "+name)
			}
		}
	}
	ignore, err := os.ReadFile(filepath.Join(c.Root, ".gitignore"))
	if err != nil || !ignoresDotenv(string(ignore)) {
		problems = append(problems, ".gitignore does not exclude .env")
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return failure(strings.Join(problems, ", "))
	}
	return doctor.Result{Status: doctor.OK, Message: "tracked secrets are encrypted and .env is ignored"}
}

func fsPath(name string) bool {
	return name != "." && !strings.HasPrefix(name, "/") && path.Clean(name) == name && !strings.HasPrefix(name, "../")
}

func encrypted(data []byte) bool {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&doc) != nil || len(doc.Content) != 1 {
		return false
	}
	var extra yaml.Node
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "sops" && root.Content[i+1].Kind == yaml.MappingNode && len(root.Content[i+1].Content) > 0 {
			return true
		}
	}
	return false
}

func ignoresDotenv(data string) bool {
	ignored := false
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negative := strings.HasPrefix(line, "!")
		pattern := strings.TrimPrefix(strings.TrimPrefix(line, "!"), "/")
		matched, _ := path.Match(pattern, ".env")
		if pattern == "**/.env" {
			matched = true
		}
		if matched {
			ignored = !negative
		}
	}
	return ignored
}

func failure(message string) doctor.Result {
	return doctor.Result{Status: doctor.Fail, Message: message, Try: "encrypt secrets with sops, remove private files from Git with git rm --cached, and add .env and *.agekey to .gitignore"}
}
