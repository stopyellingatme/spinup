// Package doctest runs console blocks in documentation pages.
package doctest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
)

// Page is a documentation page with its console blocks.
type Page struct {
	Path   string
	Blocks []Block
}

// Block is one console block and its directives.
type Block struct {
	Line        int
	Skip        bool
	RequiresEnv []string
	Steps       []Step
}

// Step is one command and its expected output.
type Step struct {
	Line     int
	Command  string
	Want     []string
	WantExit int
}

// Failure describes one failed documentation command.
type Failure struct {
	Path    string
	Line    int
	Command string
	Message string
}

// Env gives RunPage the binary directory and extra environment values.
type Env struct {
	Bin  string
	Vars []string
}

// Parse reads console blocks from a Markdown page.
func Parse(path string, data []byte) (Page, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	page := Page{Path: path}
	for index := 0; index < len(lines); index++ {
		marker, width, ok := fenceStart(lines[index])
		if !ok {
			continue
		}
		if strings.TrimSpace(lines[index][width:]) != "console" {
			for index++; index < len(lines) && !fenceEnd(lines[index], marker, width); index++ {
			}
			continue
		}
		block := Block{Line: index + 1}
		if index > 0 {
			var err error
			block, err = directive(path, index, lines[index-1], block)
			if err != nil {
				return Page{}, err
			}
		}
		closed := false
		for index++; index < len(lines); index++ {
			if fenceEnd(lines[index], marker, width) {
				closed = true
				break
			}
			if strings.HasPrefix(lines[index], "$ ") {
				block.Steps = append(block.Steps, Step{Line: index + 1, Command: strings.TrimPrefix(lines[index], "$ ")})
				continue
			}
			if len(block.Steps) != 0 {
				last := &block.Steps[len(block.Steps)-1]
				last.Want = append(last.Want, lines[index])
			}
		}
		if !closed {
			return Page{}, fmt.Errorf("%s:%d: console fence is not closed", path, block.Line)
		}
		if len(block.Steps) == 0 {
			return Page{}, fmt.Errorf("%s:%d: console block has no command", path, block.Line)
		}
		for stepIndex := range block.Steps {
			step := &block.Steps[stepIndex]
			if len(step.Want) == 0 {
				continue
			}
			last := step.Want[len(step.Want)-1]
			if code, ok := exitCode(last); ok {
				step.WantExit = code
				step.Want = step.Want[:len(step.Want)-1]
			}
		}
		page.Blocks = append(page.Blocks, block)
	}
	return page, nil
}

func directive(path string, line int, text string, block Block) (Block, error) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "<!-- doctest:") {
		return block, nil
	}
	if text == "<!-- doctest: skip -->" {
		block.Skip = true
		return block, nil
	}
	const prefix = "<!-- doctest: requires-env "
	if strings.HasPrefix(text, prefix) && strings.HasSuffix(text, " -->") {
		name := strings.TrimSuffix(strings.TrimPrefix(text, prefix), " -->")
		if validEnvName(name) {
			block.RequiresEnv = append(block.RequiresEnv, name)
			return block, nil
		}
	}
	return Block{}, fmt.Errorf("%s:%d: unknown doctest directive", path, line)
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if char == '_' || char >= 'A' && char <= 'Z' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func fenceStart(line string) (byte, int, bool) {
	if len(line) < 3 || line[0] != '`' && line[0] != '~' {
		return 0, 0, false
	}
	marker := line[0]
	width := 0
	for width < len(line) && line[width] == marker {
		width++
	}
	return marker, width, width >= 3
}

func fenceEnd(line string, marker byte, width int) bool {
	if len(line) < width || line[0] != marker {
		return false
	}
	count := 0
	for count < len(line) && line[count] == marker {
		count++
	}
	return count >= width && strings.TrimSpace(line[count:]) == ""
}

func exitCode(line string) (int, bool) {
	if !strings.HasPrefix(line, "[exit ") || !strings.HasSuffix(line, "]") {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "[exit "), "]"))
	return code, err == nil && code >= 0
}

// Match compares expected lines with command output.
func Match(want []string, got string) (bool, string) {
	gotLines := normalizeOutput(got)
	if matchLines(want, gotLines) {
		return true, ""
	}
	return false, fmt.Sprintf("output differs\nwant:\n%s\ngot:\n%s", displayLines(want), displayLines(gotLines))
}

func normalizeOutput(got string) []string {
	lines := strings.Split(strings.ReplaceAll(got, "\r\n", "\n"), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func matchLines(want, got []string) bool {
	cache := make(map[[2]int]bool)
	seen := make(map[[2]int]bool)
	var match func(int, int) bool
	match = func(wantIndex, gotIndex int) bool {
		key := [2]int{wantIndex, gotIndex}
		if seen[key] {
			return cache[key]
		}
		seen[key] = true
		var ok bool
		switch {
		case wantIndex == len(want):
			ok = gotIndex == len(got)
		case want[wantIndex] == "...":
			ok = match(wantIndex+1, gotIndex) || gotIndex < len(got) && match(wantIndex, gotIndex+1)
		default:
			ok = gotIndex < len(got) && want[wantIndex] == got[gotIndex] && match(wantIndex+1, gotIndex+1)
		}
		cache[key] = ok
		return ok
	}
	return match(0, 0)
}

func displayLines(lines []string) string {
	if len(lines) == 0 {
		return "<empty>"
	}
	return strings.Join(lines, "\n")
}

// Discover returns documentation pages that contain doctests.
func Discover(root string) ([]string, error) {
	docs := filepath.Join(root, "docs")
	var paths []string
	err := filepath.WalkDir(docs, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (path == filepath.Join(docs, "design") || path == filepath.Join(docs, "adr")) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(relative))
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	sort.Strings(paths)
	return paths, err
}

// RunPage runs every eligible console block in page.
func RunPage(ctx context.Context, runner exec.Runner, page Page, env Env) []Failure {
	directory, err := os.MkdirTemp("", "spinup-doctest-")
	if err != nil {
		return []Failure{{Path: page.Path, Message: fmt.Sprintf("make temporary directory: %v", err)}}
	}
	defer os.RemoveAll(directory)
	current := directory
	var failures []Failure
	for _, block := range page.Blocks {
		if block.Skip || missingEnv(block.RequiresEnv, env.Vars) {
			continue
		}
		for _, step := range block.Steps {
			if next, ok := cdDirectory(step.Command, current); ok {
				current = next
				continue
			}
			result, err := runner.Run(ctx, exec.Cmd{
				Name: "bash",
				Args: []string{"-c", "exec 2>&1\n" + step.Command},
				Dir:  current,
				Env:  commandEnv(env, directory),
				// A generator run builds and tests a generated app. Slow CI runners need more than two minutes.
				Timeout: 5 * time.Minute,
			})
			if result.ExitCode != step.WantExit {
				failures = append(failures, Failure{Path: page.Path, Line: step.Line, Command: step.Command, Message: fmt.Sprintf("exit code: want %d, got %d", step.WantExit, result.ExitCode)})
				break
			}
			var exitError *exec.ExitError
			if err != nil && !errors.As(err, &exitError) {
				failures = append(failures, Failure{Path: page.Path, Line: step.Line, Command: step.Command, Message: err.Error()})
				break
			}
			if ok, diff := Match(step.Want, string(result.Stdout)); !ok {
				failures = append(failures, Failure{Path: page.Path, Line: step.Line, Command: step.Command, Message: diff})
				break
			}
		}
	}
	return failures
}

func missingEnv(names, values []string) bool {
	for _, name := range names {
		if envValue(name, values) == "" {
			return true
		}
	}
	return false
}

func envValue(name string, values []string) string {
	for index := len(values) - 1; index >= 0; index-- {
		key, value, ok := strings.Cut(values[index], "=")
		if ok && key == name {
			return value
		}
	}
	return ""
}

func commandEnv(env Env, home string) []string {
	values := append([]string(nil), env.Vars...)
	path := env.Bin
	if inherited := os.Getenv("PATH"); inherited != "" {
		path += string(os.PathListSeparator) + inherited
	}
	// LC_ALL=C keeps sort order and messages the same on every machine.
	return append(values, "PATH="+path, "HOME="+home, "SPINUP_NO_UPDATE_CHECK=1", "NO_COLOR=1", "LC_ALL=C")
}

func cdDirectory(command, current string) (string, bool) {
	parts := strings.Fields(command)
	if len(parts) != 2 || parts[0] != "cd" || strings.ContainsAny(parts[1], "~$`'\"\\|&;<>*?[]{}()!") {
		return "", false
	}
	if filepath.IsAbs(parts[1]) {
		return filepath.Clean(parts[1]), true
	}
	return filepath.Join(current, parts[1]), true
}
