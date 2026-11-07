// Package devkit detects a development build of the CLI and finds the kit of its checkout.
//
// A development build renders the templates of the checkout. The generated api/go.mod pins
// the last released kit, and that kit can lack packages that the new templates import.
// The fix is a kit replace to the checkout (SPINUP_KIT_REPLACE).
package devkit

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvReplace is the environment variable that names the kit directory to replace.
const EnvReplace = "SPINUP_KIT_REPLACE"

// IsDev reports whether the CLI version names a development build.
// Release builds and "make install" builds set a real version with -ldflags.
func IsDev(version string) bool {
	v := strings.TrimPrefix(version, "v")
	return v == "dev" || strings.HasPrefix(v, "0.0.0-")
}

// Dir returns the kit directory of the checkout that built this binary, or "" when it is not found.
// It looks from the compiled source path first, then from the path of the executable.
func Dir() string {
	var starts []string
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		starts = append(starts, filepath.Dir(file))
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		starts = append(starts, filepath.Dir(exe))
	}
	for _, start := range starts {
		if dir := find(start); dir != "" {
			return dir
		}
	}
	return ""
}

// find walks up from start to a directory that holds kit/go.mod beside a go.mod of the CLI module.
func find(start string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		root, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(root), "module github.com/stopyellingatme/spinup\n") {
			kit := filepath.Join(dir, "kit")
			if _, err := os.Stat(filepath.Join(kit, "go.mod")); err == nil {
				return kit
			}
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

// Hint returns the action that fixes a missing kit replace, with the checkout kit when it is known.
func Hint() string {
	dir := Dir()
	if dir == "" {
		dir = "<spinup checkout>/kit"
	}
	return EnvReplace + "=" + dir
}
