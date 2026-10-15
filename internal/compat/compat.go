// Package compat checks CLI, kit, and template version compatibility.
package compat

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Version is a semantic version without build metadata.
type Version struct {
	Major int
	Minor int
	Patch int
	Pre   string
}

// ParseVersion parses a semantic version with an optional v prefix and prerelease.
func ParseVersion(s string) (Version, error) {
	original := s
	s = strings.TrimPrefix(s, "v")
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid version %q: expected X.Y.Z", original)
	}
	values := [3]int{}
	for index, part := range parts {
		value, err := parseNumber(part)
		if err != nil {
			return Version{}, fmt.Errorf("invalid version %q: %w", original, err)
		}
		values[index] = value
	}
	if hasPre && !validPrerelease(pre) {
		return Version{}, fmt.Errorf("invalid version %q: invalid prerelease", original)
	}
	return Version{Major: values[0], Minor: values[1], Patch: values[2], Pre: pre}, nil
}

// Compare reports whether v sorts before, equal to, or after o.
func (v Version) Compare(o Version) int {
	if result := compareInt(v.Major, o.Major); result != 0 {
		return result
	}
	if result := compareInt(v.Minor, o.Minor); result != 0 {
		return result
	}
	if result := compareInt(v.Patch, o.Patch); result != 0 {
		return result
	}
	if v.Pre == "" && o.Pre != "" {
		return 1
	}
	if v.Pre != "" && o.Pre == "" {
		return -1
	}
	if v.Pre == "" {
		return 0
	}
	return comparePrerelease(v.Pre, o.Pre)
}

// String returns the canonical semantic version with a v prefix.
func (v Version) String() string {
	value := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		return value + "-" + v.Pre
	}
	return value
}

// CheckKit verifies that CLI and kit major and minor versions match.
func CheckKit(cliVersion, kitVersion string) error {
	if cliVersion == "dev" {
		return nil
	}
	cli, err := ParseVersion(cliVersion)
	if err != nil {
		return fmt.Errorf("cannot check CLI compatibility: %w\nTry: spinup upgrade", err)
	}
	kit, err := ParseVersion(kitVersion)
	if err != nil {
		return fmt.Errorf("cannot check kit compatibility: %w\nTry: go get github.com/stopyellingatme/spinup/kit@v%d.%d.x", err, cli.Major, cli.Minor)
	}
	if cli.Major == kit.Major && cli.Minor == kit.Minor {
		return nil
	}
	return fmt.Errorf("CLI %s is incompatible with kit %s: major and minor versions must match\nTry: spinup upgrade or go get github.com/stopyellingatme/spinup/kit@v%d.%d.x", cli, kit, cli.Major, cli.Minor)
}

// SupportedTemplateVersions returns up to three released templates supported by the CLI.
func SupportedTemplateVersions(cliVersion string, released []string) []string {
	cliIsDev := cliVersion == "dev"
	cli, err := ParseVersion(cliVersion)
	if err != nil && !cliIsDev {
		return nil
	}
	type candidate struct {
		raw     string
		version Version
	}
	candidates := make([]candidate, 0, len(released))
	for _, raw := range released {
		version, err := ParseVersion(raw)
		if err != nil || (!cliIsDev && version.Compare(cli) > 0) {
			continue
		}
		candidates = append(candidates, candidate{raw: raw, version: version})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if result := candidates[i].version.Compare(candidates[j].version); result != 0 {
			return result > 0
		}
		return candidates[i].raw < candidates[j].raw
	})
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}
	result := make([]string, len(candidates))
	for index, candidate := range candidates {
		result[index] = candidate.raw
	}
	return result
}

// CheckMinCLI verifies that the CLI satisfies a template minimum version.
func CheckMinCLI(cliVersion, minCLI string) error {
	if cliVersion == "dev" {
		return nil
	}
	cli, err := ParseVersion(cliVersion)
	if err != nil {
		return fmt.Errorf("cannot check CLI version: %w\nTry: spinup self-update", err)
	}
	minimum, err := ParseVersion(minCLI)
	if err != nil {
		return fmt.Errorf("cannot check minimum CLI version: %w\nTry: spinup self-update", err)
	}
	if cli.Compare(minimum) < 0 {
		return fmt.Errorf("CLI %s is older than required version %s\nTry: spinup self-update", cli, minimum)
	}
	return nil
}

func parseNumber(s string) (int, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, fmt.Errorf("invalid numeric identifier %q", s)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid numeric identifier %q", s)
		}
	}
	value, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric identifier %q", s)
	}
	return value, nil
}

func validPrerelease(pre string) bool {
	if pre == "" {
		return false
	}
	for _, identifier := range strings.Split(pre, ".") {
		if identifier == "" {
			return false
		}
		for _, r := range identifier {
			if !isASCIIAlphaNumeric(r) && r != '-' {
				return false
			}
		}
		if isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func comparePrerelease(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		l, r := leftParts[index], rightParts[index]
		lNumeric, rNumeric := isNumeric(l), isNumeric(r)
		switch {
		case lNumeric && rNumeric:
			if result := compareNumericIdentifier(l, r); result != 0 {
				return result
			}
		case lNumeric:
			return -1
		case rNumeric:
			return 1
		case l < r:
			return -1
		case l > r:
			return 1
		}
	}
	return compareInt(len(leftParts), len(rightParts))
}

func isASCIIAlphaNumeric(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func compareNumericIdentifier(left, right string) int {
	if result := compareInt(len(left), len(right)); result != 0 {
		return result
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func compareInt(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
