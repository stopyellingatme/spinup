package pack

import (
	"bytes"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// ExecConfig extends the immutable plugin metadata contract for executable plugins.
type ExecConfig struct {
	Kind     string
	Binaries map[string]string
}

type metadata struct {
	plugin.Meta
	Kind string `toml:"kind"`
	Exec *struct {
		Binaries map[string]string `toml:"binaries"`
	} `toml:"exec"`
}

// ReadExec reads and validates the executable extension of plugin.toml.
// A missing kind means pack. No executable code runs during validation.
func ReadExec(fsys fs.FS) (ExecConfig, error) {
	var result ExecConfig
	if fsys == nil {
		return result, problem("read exec metadata", fmt.Errorf("plugin filesystem is missing"))
	}
	data, err := fs.ReadFile(fsys, "plugin.toml")
	if err != nil {
		return result, problem("read exec metadata", err)
	}
	var meta metadata
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&meta); err != nil {
		return result, problem("decode plugin metadata", err)
	}
	result.Kind = meta.Kind
	if result.Kind == "" {
		result.Kind = "pack"
	}
	switch result.Kind {
	case "pack":
		if meta.Exec != nil {
			return result, problem("validate plugin kind", fmt.Errorf("pack plugins cannot declare [exec]"))
		}
	case "exec":
		if meta.Exec == nil || len(meta.Exec.Binaries) == 0 {
			return result, problem("validate exec binaries", fmt.Errorf("exec plugins require [exec] binaries"))
		}
		result.Binaries = meta.Exec.Binaries
		platforms := make([]string, 0, len(result.Binaries))
		for platform := range result.Binaries {
			platforms = append(platforms, platform)
		}
		slices.Sort(platforms)
		for _, platform := range platforms {
			name := result.Binaries[platform]
			parts := strings.Split(platform, "-")
			if len(parts) != 2 || !knownPlatform(parts[0], parts[1]) {
				return result, problem("validate exec platform", fmt.Errorf("invalid platform %q", platform))
			}
			clean, err := vfs.CleanPath(name)
			if err != nil || clean != name || !strings.HasPrefix(name, "bin/") {
				return result, problem("validate exec binary", fmt.Errorf("binary %q must be a clean path inside bin/", name))
			}
			info, err := fs.Stat(fsys, name)
			if err != nil {
				return result, problem("validate exec binary", err)
			}
			if !info.Mode().IsRegular() || parts[0] != "windows" && info.Mode().Perm()&0111 == 0 {
				return result, problem("validate exec binary", fmt.Errorf("binary %q must be an executable regular file", name))
			}
		}
	default:
		return result, problem("validate plugin kind", fmt.Errorf("unsupported kind %q", result.Kind))
	}
	return result, nil
}

func knownPlatform(goos, arch string) bool {
	// The keys use Go's GOOS-GOARCH spelling, including cross-compiled artifacts.
	return slices.Contains([]string{"aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows"}, goos) && slices.Contains([]string{"386", "amd64", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "wasm"}, arch)
}
