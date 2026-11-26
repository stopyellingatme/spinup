package run

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	exec "github.com/stopyellingatme/spinup/internal/exec"
)

// ToolCommand returns a cached Go tool command, or the original go tool command.
// The cache is outside the project. SPINUP_NO_TOOL_CACHE=1 disables it.
// Local replacements and active workspaces bypass the cache.
// Resolution failures leave tool execution to go tool, which reports its errors.
func ToolCommand(ctx context.Context, dir string, runner exec.Runner, name string, args ...string) exec.Cmd {
	fallback := exec.Cmd{Name: "go", Args: append([]string{"tool", "-modfile=tools.mod", name}, args...), Dir: dir}
	if os.Getenv("SPINUP_NO_TOOL_CACHE") == "1" {
		return fallback
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fallback
	}
	key, err := toolKey(ctx, dir, runner, name)
	if err != nil {
		return fallback
	}
	cacheDir = filepath.Join(cacheDir, "spinup", "tools")
	cachePath := filepath.Join(cacheDir, key)
	if data, err := os.ReadFile(cachePath); err == nil {
		if path := strings.TrimSpace(string(data)); executableTool(path) {
			return exec.Cmd{Name: path, Args: args, Dir: dir}
		}
	}
	result, err := runner.Run(ctx, exec.Cmd{Name: "go", Args: []string{"tool", "-modfile=tools.mod", "-n", name}, Dir: dir})
	if err != nil || result.ExitCode != 0 {
		return fallback
	}
	path := strings.TrimSpace(string(result.Stdout))
	if !executableTool(path) {
		return fallback
	}
	if err := saveToolPath(cacheDir, cachePath, path); err != nil {
		return fallback
	}
	return exec.Cmd{Name: path, Args: args, Dir: dir}
}

func toolKey(ctx context.Context, dir string, runner exec.Runner, name string) (string, error) {
	mod, err := os.ReadFile(filepath.Join(dir, "tools.mod"))
	if err != nil {
		return "", err
	}
	sum, err := os.ReadFile(filepath.Join(dir, "tools.sum"))
	if err != nil {
		return "", err
	}
	values, err := toolEnvironment(ctx, dir, runner)
	if err != nil {
		return "", err
	}
	if values.GOWORK != "" && values.GOWORK != "off" {
		return "", fmt.Errorf("go tool cache is disabled for workspaces")
	}
	// Ask Go to parse replacements, including quoted paths and block directives.
	if strings.Contains(string(mod), "replace") {
		result, err := runner.Run(ctx, exec.Cmd{Name: "go", Args: []string{"mod", "edit", "-json", "-modfile=tools.mod"}, Dir: dir})
		if err != nil {
			return "", err
		}
		var module struct {
			Replace []struct {
				New struct{ Path, Version string }
			}
		}
		if err := json.Unmarshal(result.Stdout, &module); err != nil {
			return "", err
		}
		if result.ExitCode != 0 {
			return "", fmt.Errorf("go tool replacements are unavailable")
		}
		for _, replacement := range module.Replace {
			if replacement.New.Version == "" {
				return "", fmt.Errorf("go tool cache is disabled for local replacements")
			}
		}
	}
	// JSON frames each input, so different input boundaries cannot share a key.
	data, err := json.Marshal([][]byte{[]byte("v2"), mod, sum, []byte(values.GOVERSION), []byte(values.GOOS), []byte(values.GOARCH), []byte(values.GOFLAGS), []byte(values.GOCACHE), []byte(name)})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

type goEnvironment struct {
	GOOS, GOARCH, GOFLAGS, GOCACHE, GOWORK, GOVERSION string
}

var environmentCache sync.Map

func toolEnvironment(ctx context.Context, dir string, runner exec.Runner) (goEnvironment, error) {
	if err := ctx.Err(); err != nil {
		return goEnvironment{}, err
	}
	// Cache only the real runner. Test runners and other implementations can
	// return different environments on each call.
	_, cacheable := runner.(exec.OSRunner)
	cacheKey := dir + "\x00" + strings.Join(os.Environ(), "\x00")
	// A workspace can appear after the first lookup. Probe again in that case.
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Stat(filepath.Join(current, "go.work"))
		if err == nil {
			cacheKey += fmt.Sprintf("\x00%s:%d:%d", current, info.Size(), info.ModTime().UnixNano())
		} else if !os.IsNotExist(err) {
			cacheable = false
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	if cacheable {
		if value, ok := environmentCache.Load(cacheKey); ok {
			return value.(goEnvironment), nil
		}
	}
	result, err := runner.Run(ctx, exec.Cmd{Name: "go", Args: []string{"env", "-json", "GOOS", "GOARCH", "GOFLAGS", "GOCACHE", "GOWORK", "GOVERSION"}, Dir: dir})
	if err != nil {
		return goEnvironment{}, err
	}
	var values goEnvironment
	if err := json.Unmarshal(result.Stdout, &values); err != nil {
		return goEnvironment{}, err
	}
	if result.ExitCode != 0 || values.GOVERSION == "" || values.GOOS == "" || values.GOARCH == "" || values.GOCACHE == "" {
		return goEnvironment{}, fmt.Errorf("go tool environment is unavailable")
	}
	if cacheable && values.GOWORK == "off" {
		environmentCache.Store(cacheKey, values)
	}
	return values, nil
}

func executableTool(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(path), ".exe")
	}
	return info.Mode().Perm()&0111 != 0
}

func saveToolPath(dir, filename, path string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".tool-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(path + "\n"); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filename)
}
