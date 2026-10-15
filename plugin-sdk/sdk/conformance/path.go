package conformance

import (
	"fmt"
	"io/fs"
	"strings"
	"unicode"
)

// safePath mirrors the host's portable path and protected-file rules without
// importing the CLI module. The SDK has only standard-library dependencies.
func safePath(name string) error {
	invalid := func() error { return fmt.Errorf("unsafe output path %q", name) }
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, `:\`) || strings.ContainsFunc(name, unicode.IsControl) {
		return invalid()
	}
	parts := strings.Split(strings.ToLower(name), "/")
	if parts[0] == ".spinup" || parts[0] == ".github" || len(parts) == 1 && (parts[0] == "spinup.toml" || parts[0] == "spinup.lock") {
		return invalid()
	}
	for _, part := range parts {
		if part == ".git" || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return invalid()
		}
		base, _, _ := strings.Cut(part, ".")
		if base == "con" || base == "prn" || base == "aux" || base == "nul" || len(base) == 4 && base[3] >= '1' && base[3] <= '9' && (base[:3] == "com" || base[:3] == "lpt") {
			return invalid()
		}
	}
	return nil
}
