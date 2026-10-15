package store

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

var metadataPattern = regexp.MustCompile(`^[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*$`)

// CurrentVersion returns the semantic template version of this CLI build without v.
func CurrentVersion(cliVersion string) string {
	version := strings.TrimPrefix(cliVersion, "v")
	if validateVersion(version) != nil {
		return "0.0.0-dev"
	}
	return version
}

// Current returns the template packs and stack versions embedded in this build.
func Current(cliVersion string) *templates.Version {
	return &templates.Version{Version: CurrentVersion(cliVersion), Packs: templates.Embedded(), Stack: stackversions.MustLoad()}
}

func validateVersion(version string) error {
	core, metadata, hasMetadata := strings.Cut(version, "+")
	_, err := compat.ParseVersion(core)
	if strings.HasPrefix(version, "v") || err != nil || (hasMetadata && !metadataPattern.MatchString(metadata)) {
		return fmt.Errorf("invalid template version %q: expected a semantic version without v\nTry: use a version such as 0.2.0", version)
	}
	return nil
}
