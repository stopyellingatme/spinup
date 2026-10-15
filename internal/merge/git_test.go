package merge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
)

func TestGitMergerMerge(t *testing.T) {
	t.Parallel()

	base := "package demo\n\nfunc main() {\n\tfmt.Println(\"old\")\n}\n"
	tests := []struct {
		name       string
		input      Input
		conflicted bool
		contains   []string
	}{
		{
			name:     "no change on either side",
			input:    Input{Path: "main.go", Base: []byte(base), Ours: []byte(base), Theirs: []byte(base)},
			contains: []string{"fmt.Println(\"old\")"},
		},
		{
			name:     "ours equals base",
			input:    Input{Path: "main.go", Base: []byte(base), Ours: []byte(base), Theirs: []byte(strings.Replace(base, "old", "new", 1))},
			contains: []string{"fmt.Println(\"new\")"},
		},
		{
			name:     "theirs equals base",
			input:    Input{Path: "main.go", Base: []byte(base), Ours: []byte(strings.Replace(base, "old", "mine", 1)), Theirs: []byte(base)},
			contains: []string{"fmt.Println(\"mine\")"},
		},
		{
			name:     "both sides make the same change",
			input:    Input{Path: "main.go", Base: []byte(base), Ours: []byte(strings.Replace(base, "old", "same", 1)), Theirs: []byte(strings.Replace(base, "old", "same", 1))},
			contains: []string{"fmt.Println(\"same\")"},
		},
		{
			name: "independent Go edits",
			input: Input{
				Path:   "api/main.go",
				Base:   []byte("package api\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"),
				Ours:   []byte("package api\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n\nfunc greet() {\n\tfmt.Println(\"hi\")\n}\n"),
				Theirs: []byte("package api\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc main() {\n\tfmt.Println(strings.ToUpper(\"hello\"))\n}\n"),
			},
			contains: []string{"\"strings\"", "func greet()"},
		},
		{
			name: "YAML conflict has diff3 labels",
			input: Input{
				Path:   "deploy/config.yml",
				Base:   []byte("service:\n  port: 8080\n"),
				Ours:   []byte("service:\n  port: 8081\n"),
				Theirs: []byte("service:\n  port: 9090\n"),
			},
			conflicted: true,
			contains: []string{
				"<<<<<<< deploy/config.yml (yours)",
				"||||||| deploy/config.yml (base)",
				"=======",
				">>>>>>> deploy/config.yml (new template)",
			},
		},
		{
			name: "independent JSON keys",
			input: Input{
				Path:   "web/package.json",
				Base:   []byte("{\n  \"name\": \"web\",\n  \"scripts\": {\n    \"check\": \"svelte-check\"\n  }\n}\n"),
				Ours:   []byte("{\n  \"name\": \"web\",\n  \"private\": true,\n  \"scripts\": {\n    \"check\": \"svelte-check\"\n  }\n}\n"),
				Theirs: []byte("{\n  \"name\": \"web\",\n  \"scripts\": {\n    \"check\": \"svelte-check\",\n    \"build\": \"vite build\"\n  }\n}\n"),
			},
			contains: []string{"\"private\": true", "\"build\": \"vite build\""},
		},
		{
			name: "independent Svelte edits",
			input: Input{
				Path:   "web/src/App.svelte",
				Base:   []byte("<script>\n  let name = 'world';\n</script>\n\n<h1>Hello {name}</h1>\n"),
				Ours:   []byte("<script>\n  let name = 'spinup';\n</script>\n\n<h1>Hello {name}</h1>\n"),
				Theirs: []byte("<script>\n  let name = 'world';\n</script>\n\n<main><h1>Hello {name}</h1></main>\n"),
			},
			contains: []string{"'spinup'", "<main>"},
		},
		{
			name: "caller normalized CRLF to LF",
			input: Input{
				Path:   "README.md",
				Base:   []byte(normalizeLF("title\r\nfirst\r\nsecond\r\nthird\r\nlast\r\n")),
				Ours:   []byte(normalizeLF("title\r\nours\r\nsecond\r\nthird\r\nlast\r\n")),
				Theirs: []byte(normalizeLF("title\r\nfirst\r\nsecond\r\nthird\r\ntheirs\r\n")),
			},
			contains: []string{"ours\n", "theirs\n"},
		},
		{
			name:     "new template file with equal content",
			input:    Input{Path: "new.txt", Ours: []byte("same\n"), Theirs: []byte("same\n")},
			contains: []string{"same\n"},
		},
		{
			name:       "new template file with different content conflicts",
			input:      Input{Path: "new.txt", Ours: []byte("ours\n"), Theirs: []byte("theirs\n")},
			conflicted: true,
			contains:   []string{"<<<<<<< new.txt (yours)", ">>>>>>> new.txt (new template)"},
		},
		{
			name: "theirs deletes an unchanged hunk",
			input: Input{
				Path:   "notes.txt",
				Base:   []byte("keep\ndelete\nlast\n"),
				Ours:   []byte("keep\ndelete\nlast\n"),
				Theirs: []byte("keep\nlast\n"),
			},
			contains: []string{"keep\nlast\n"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result, err := NewGit(exec.OSRunner{}).Merge(context.Background(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Conflicted != test.conflicted {
				t.Errorf("Conflicted = %t, want %t", result.Conflicted, test.conflicted)
			}
			for _, want := range test.contains {
				if !strings.Contains(string(result.Data), want) {
					t.Errorf("merged data does not contain %q:\n%s", want, result.Data)
				}
			}
		})
	}
}

func TestGitMergerRejectsUnsafeInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input Input
	}{
		{name: "NUL byte", input: Input{Path: "a.txt", Base: []byte("ok\x00\n")}},
		{name: "large input", input: Input{Path: "a.txt", Ours: make([]byte, MaxSize+1)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewGit(exec.OSRunner{}).Merge(context.Background(), test.input)
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Errorf("Merge() error = %v, want a guided error", err)
			}
		})
	}
}

func TestGitMergerReportsRunnerFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		exitCode int
		err      error
		wantTry  bool
	}{
		{name: "conflict exit", exitCode: 1},
		{name: "capped conflict exit", exitCode: 127},
		{name: "invalid exit", exitCode: 128, wantTry: true},
		{name: "start failure", err: errors.New("start failed"), wantTry: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &exec.Fake{}
			expectation := fake.Expect("git", "merge-file")
			if test.err != nil {
				expectation.Fail(test.err)
			} else {
				expectation.Return("merged\n", "", test.exitCode)
			}

			result, err := NewGit(fake).Merge(context.Background(), Input{Path: "a.txt"})
			fake.AssertDone(t)
			if test.wantTry {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Errorf("Merge() error = %v, want a guided error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !result.Conflicted {
				t.Error("Conflicted = false, want true")
			}
		})
	}
}

func TestGitMergerRunsExpectedCommand(t *testing.T) {
	t.Parallel()

	fake := &exec.Fake{}
	fake.Expect("git", "merge-file").Return("merged\n", "", 0)
	result, err := NewGit(fake).Merge(context.Background(), Input{
		Path:   "api/main.go",
		Base:   []byte("base\n"),
		Ours:   []byte("ours\n"),
		Theirs: []byte("theirs\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Data) != "merged\n" || result.Conflicted {
		t.Errorf("Merge() = %+v, want a clean merged result", result)
	}
	fake.AssertDone(t)

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(calls))
	}
	arguments := calls[0].Args
	wantPrefix := []string{
		"merge-file", "-p", "--diff3", "--diff-algorithm=histogram",
		"-L", "api/main.go (yours)",
		"-L", "api/main.go (base)",
		"-L", "api/main.go (new template)",
	}
	if len(arguments) != len(wantPrefix)+3 || !reflect.DeepEqual(arguments[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("git arguments = %#v, want prefix %#v and three input paths", arguments, wantPrefix)
	}
	for index, wantName := range []string{"ours", "base", "theirs"} {
		inputPath := arguments[len(wantPrefix)+index]
		if filepath.Base(inputPath) != wantName {
			t.Errorf("input path %d = %q, want %q", index, inputPath, wantName)
		}
		if _, err := os.Stat(inputPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("input path %q remains after Merge: %v", inputPath, err)
		}
	}
}

func TestCheckGitVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		output  string
		err     error
		want    string
		wantErr string
	}{
		{name: "standard", output: "git version 2.56.0\n", want: "2.56.0"},
		{name: "Apple Git", output: "git version 2.39.5 (Apple Git-154)\n", wantErr: "found 2.39.5, but requires 2.44.0"},
		{name: "Windows Git", output: "git version 2.44.0.windows.1\n", want: "2.44.0"},
		{name: "too old", output: "git version 2.43.9\n", wantErr: "found 2.43.9, but requires 2.44.0"},
		{name: "missing", err: &exec.NotFoundError{Name: "git"}, wantErr: "Try: install git 2.44 or later"},
		{name: "garbage", output: "unknown\n", wantErr: "Try: install git 2.44 or later"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := &exec.Fake{}
			expectation := fake.Expect("git", "--version")
			if test.err != nil {
				expectation.Fail(test.err)
			} else {
				expectation.Return(test.output, "", 0)
			}

			got, err := CheckGitVersion(context.Background(), fake)
			fake.AssertDone(t)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Errorf("CheckGitVersion() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("CheckGitVersion() = %q, want %q", got, test.want)
			}
		})
	}
}

func normalizeLF(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}
