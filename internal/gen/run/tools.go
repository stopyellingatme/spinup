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

	exec "github.com/stopyellingatme/spinup/internal/exec"
)

// ToolCommand returns a cached Go tool command, or the original go tool command.
// The cache is outside the project. SPINUP_NO_TOOL_CACHE=1 disables it.
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
	version, err := runner.Run(ctx, exec.Cmd{Name: "go", Args: []string{"version"}, Dir: dir})
	if err != nil {
		return "", err
	}
	environment, err := runner.Run(ctx, exec.Cmd{Name: "go", Args: []string{"env", "-json", "GOOS", "GOARCH", "GOFLAGS", "GOCACHE"}, Dir: dir})
	if err != nil {
		return "", err
	}
	var values struct {
		GOOS, GOARCH, GOFLAGS, GOCACHE string
	}
	if err := json.Unmarshal(environment.Stdout, &values); err != nil {
		return "", err
	}
	if version.ExitCode != 0 || environment.ExitCode != 0 || len(version.Stdout) == 0 || values.GOOS == "" || values.GOARCH == "" || values.GOCACHE == "" {
		return "", fmt.Errorf("go tool environment is unavailable")
	}
	// JSON frames each input, so different input boundaries cannot share a key.
	data, err := json.Marshal([][]byte{mod, sum, version.Stdout, []byte(values.GOOS), []byte(values.GOARCH), []byte(values.GOFLAGS), []byte(values.GOCACHE), []byte(name)})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
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
