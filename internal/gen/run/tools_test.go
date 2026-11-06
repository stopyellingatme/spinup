package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	exec "github.com/stopyellingatme/spinup/internal/exec"
)

const testGoEnv = `{"GOOS":"linux","GOARCH":"amd64","GOFLAGS":"-mod=readonly","GOCACHE":"/cache/go"}`

func toolCacheFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	t.Setenv("LocalAppData", home)
	t.Setenv("SPINUP_NO_TOOL_CACHE", "")
	dir := t.TempDir()
	put(t, dir, "tools.mod", "module tools\n")
	put(t, dir, "tools.sum", "tool v1 h1:sum\n")
	binary := filepath.Join(t.TempDir(), "tool.exe")
	if err := os.WriteFile(binary, []byte("cached binary"), 0755); err != nil {
		t.Fatal(err)
	}
	return dir, binary
}

func expectToolEnvironment(fake *exec.Fake, version, environment string) {
	fake.Expect("go", "version").Return(version, "", 0)
	fake.Expect("go", "env", "-json", "GOOS", "GOARCH", "GOFLAGS", "GOCACHE").Return(environment, "", 0)
}

func TestToolCommandCache(t *testing.T) {
	dir, binary := toolCacheFixture(t)
	for i := range 2 {
		fake := &exec.Fake{}
		expectToolEnvironment(fake, "go version go1.27.2 linux/amd64", testGoEnv)
		if i == 0 {
			fake.Expect("go", "tool", "-modfile=tools.mod", "-n", "sqlc").Return(binary+"\n", "", 0)
		}
		cmd := ToolCommand(context.Background(), dir, fake, "sqlc", "generate")
		if cmd.Name != binary || cmd.Dir != dir || !reflect.DeepEqual(cmd.Args, []string{"generate"}) {
			t.Fatalf("command = %+v", cmd)
		}
		fake.Expect(binary, "generate")
		if _, err := fake.Run(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		fake.AssertDone(t)
	}
	if _, err := os.Stat(filepath.Join(dir, "spinup")); !os.IsNotExist(err) {
		t.Fatalf("cache created in project: %v", err)
	}
}

func TestToolCommandKeyInputs(t *testing.T) {
	for _, field := range []string{"tools.mod", "tools.sum", "version", "GOOS", "GOARCH", "GOFLAGS", "GOCACHE"} {
		t.Run(field, func(t *testing.T) {
			dir, binary := toolCacheFixture(t)
			version, environment := "go version go1.27.2 linux/amd64", testGoEnv
			for i := range 2 {
				if i == 1 {
					switch field {
					case "tools.mod", "tools.sum":
						put(t, dir, field, "changed\n")
					case "version":
						version = "go version go1.27.3 linux/amd64"
					default:
						environment = testGoEnv[:len(testGoEnv)-1] + `,"` + field + `":"changed"}`
					}
				}
				fake := &exec.Fake{}
				expectToolEnvironment(fake, version, environment)
				fake.Expect("go", "tool", "-modfile=tools.mod", "-n", "sqlc").Return(binary, "", 0)
				if got := ToolCommand(context.Background(), dir, fake, "sqlc").Name; got != binary {
					t.Fatalf("tool = %q", got)
				}
				fake.AssertDone(t)
			}
		})
	}
}

func TestToolCommandMissingBinaryFallsBack(t *testing.T) {
	dir, binary := toolCacheFixture(t)
	fake := &exec.Fake{}
	expectToolEnvironment(fake, "go version go1.27.2 linux/amd64", testGoEnv)
	fake.Expect("go", "tool", "-modfile=tools.mod", "-n", "sqlc").Return(binary, "", 0)
	if got := ToolCommand(context.Background(), dir, fake, "sqlc").Name; got != binary {
		t.Fatalf("tool = %q", got)
	}
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	expectToolEnvironment(fake, "go version go1.27.2 linux/amd64", testGoEnv)
	fake.Expect("go", "tool", "-modfile=tools.mod", "-n", "sqlc").Fail(errors.New("cache evicted"))
	cmd := ToolCommand(context.Background(), dir, fake, "sqlc", "generate")
	if cmd.Name != "go" || !reflect.DeepEqual(cmd.Args, []string{"tool", "-modfile=tools.mod", "sqlc", "generate"}) {
		t.Fatalf("fallback = %+v", cmd)
	}
	fake.AssertDone(t)
}

func TestToolCommandDisabled(t *testing.T) {
	dir, _ := toolCacheFixture(t)
	t.Setenv("SPINUP_NO_TOOL_CACHE", "1")
	fake := &exec.Fake{}
	if cmd := ToolCommand(context.Background(), dir, fake, "sqlc", "generate"); cmd.Name != "go" || len(fake.Calls()) != 0 {
		t.Fatalf("disabled cache: %+v, calls = %+v", cmd, fake.Calls())
	}
}

func TestToolCommandResolutionFailures(t *testing.T) {
	for _, failure := range []string{"missing sum", "version", "environment", "invalid environment", "resolve", "relative path", "missing path", "nonexecutable", "cache write"} {
		t.Run(failure, func(t *testing.T) {
			dir, binary := toolCacheFixture(t)
			fake := &exec.Fake{}
			switch failure {
			case "missing sum":
				if err := os.Remove(filepath.Join(dir, "tools.sum")); err != nil {
					t.Fatal(err)
				}
			case "version":
				fake.Expect("go", "version").Fail(errors.New("version unavailable"))
			case "environment":
				fake.Expect("go", "version").Return("go1.27.2", "", 0)
				fake.Expect("go", "env").Fail(errors.New("environment unavailable"))
			case "invalid environment":
				expectToolEnvironment(fake, "go1.27.2", "not JSON")
			default:
				expectToolEnvironment(fake, "go1.27.2", testGoEnv)
				path := binary
				switch failure {
				case "resolve":
					fake.Expect("go", "tool", "-modfile=tools.mod", "-n", "sqlc").Fail(errors.New("resolve unavailable"))
				case "relative path":
					path = "relative/tool"
				case "missing path":
					path += "-missing"
				case "nonexecutable":
					if runtime.GOOS == "windows" {
						path = dir
					} else if err := os.Chmod(binary, 0644); err != nil {
						t.Fatal(err)
					}
				case "cache write":
					cache, err := os.UserCacheDir()
					if err != nil {
						t.Fatal(err)
					}
					put(t, cache, "spinup", "a file blocks the cache directory")
				}
				if failure != "resolve" {
					fake.Expect("go", "tool", "-modfile=tools.mod", "-n", "sqlc").Return(path, "", 0)
				}
			}
			cmd := ToolCommand(context.Background(), dir, fake, "sqlc", "generate")
			if cmd.Name != "go" {
				t.Fatalf("fallback = %+v", cmd)
			}
			fake.AssertDone(t)
		})
	}
}

func TestToolCommandReresolvesMissingBinary(t *testing.T) {
	dir, binary := toolCacheFixture(t)
	for i := range 2 {
		fake := &exec.Fake{}
		expectToolEnvironment(fake, "go1.27.2", testGoEnv)
		fake.Expect("go", "tool", "-modfile=tools.mod", "-n", "sqlc").Return(binary, "", 0)
		if cmd := ToolCommand(context.Background(), dir, fake, "sqlc", "generate"); cmd.Name != binary {
			t.Fatalf("resolved command = %+v", cmd)
		}
		fake.AssertDone(t)
		if i == 0 {
			next := filepath.Join(filepath.Dir(binary), "replacement.exe")
			if err := os.Rename(binary, next); err != nil {
				t.Fatal(err)
			}
			binary = next
		}
	}
}
