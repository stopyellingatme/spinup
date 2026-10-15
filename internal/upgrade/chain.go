package upgrade

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// Chain returns the latest patch in each intervening minor, followed by the target.
// An empty target selects the newest loadable version. A nil list uses Source.Versions.
func Chain(source templates.Source, from, to string, versions []string) ([]string, error) {
	return chain(context.Background(), source, from, to, versions)
}

func chain(ctx context.Context, source templates.Source, from, to string, versions []string) ([]string, error) {
	old, err := chainVersion(from)
	if err != nil {
		return nil, err
	}
	if to != "" {
		target, err := chainVersion(to)
		if err != nil {
			return nil, err
		}
		if target.Compare(old) < 0 {
			return nil, fmt.Errorf("upgrade %s to %s: downgrades are not supported\nTry: select a version at least %s", from, to, from)
		}
		if target.Compare(old) == 0 {
			return nil, nil
		}
	}
	if source == nil {
		return nil, fmt.Errorf("select upgrade versions: template source is nil\nTry: configure a template source")
	}
	if versions == nil {
		versions, err = source.Versions(ctx)
		if err != nil {
			return nil, err
		}
	}
	type candidate struct {
		raw     string
		version compat.Version
	}
	candidates := make([]candidate, 0, len(versions))
	for _, raw := range versions {
		v, e := chainVersion(raw)
		if e != nil {
			return nil, e
		}
		candidates = append(candidates, candidate{raw, v})
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if cmp := a.version.Compare(b.version); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.raw, b.raw)
	})
	if to == "" {
		for i := len(candidates) - 1; i >= 0; i-- {
			_, e := source.Load(ctx, candidates[i].raw)
			if e == nil {
				to = candidates[i].raw
				break
			}
		}
		if to == "" {
			return nil, fmt.Errorf("select upgrade target: no template version is loadable\nTry: spinup cache warm")
		}
	}
	target, err := chainVersion(to)
	if err != nil {
		return nil, err
	}
	if target.Compare(old) < 0 {
		return nil, fmt.Errorf("upgrade %s to %s: downgrades are not supported\nTry: select a version at least %s", from, to, from)
	}
	if target.Compare(old) == 0 {
		return nil, nil
	}
	if _, err := source.Load(ctx, to); err != nil {
		return nil, err
	}
	var result []string
	for _, c := range candidates {
		if c.version.Compare(old) <= 0 || c.version.Compare(target) >= 0 {
			continue
		}
		// The target minor is always represented by the exact requested target.
		if (c.version.Major == old.Major && c.version.Minor == old.Minor) || (c.version.Major == target.Major && c.version.Minor == target.Minor) {
			continue
		}
		if len(result) > 0 {
			previous, _ := chainVersion(result[len(result)-1])
			if previous.Major == c.version.Major && previous.Minor == c.version.Minor {
				result[len(result)-1] = c.raw
				continue
			}
		}
		result = append(result, c.raw)
	}
	for _, version := range result {
		if _, err := source.Load(ctx, version); err != nil {
			return nil, err
		}
	}
	return append(result, to), nil
}

func chainVersion(raw string) (compat.Version, error) {
	core, metadata, hasMetadata := strings.Cut(raw, "+")
	validMetadata := true
	if hasMetadata {
		for _, part := range strings.Split(metadata, ".") {
			if part == "" {
				validMetadata = false
			}
			for _, char := range part {
				validMetadata = validMetadata && (char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-')
			}
		}
	}
	v, err := compat.ParseVersion(core)
	if err != nil || !validMetadata || strings.HasPrefix(raw, "v") || strings.ContainsAny(raw, "/\\\x00") {
		return compat.Version{}, fmt.Errorf("select template version %q: expected a semantic version without v\nTry: use a version such as 0.2.0", raw)
	}
	return v, nil
}
