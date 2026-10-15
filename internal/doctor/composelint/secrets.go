package composelint

import (
	"path/filepath"
	"regexp"
	"strings"
)

var secretDefault = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(?::-|-)([^}]*)\}`)
var urlCredentials = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://([^/\s@]+)@`)

func literalSecret(file, key, value string) bool {
	dev := filepath.Base(file) == "compose.dev.yml" || filepath.Base(file) == "compose.dev.yaml"
	if secretName.MatchString(key) && !strings.HasSuffix(strings.ToUpper(key), "_FILE") && value != "" && literalValue(value, dev) {
		return true
	}
	for _, match := range urlCredentials.FindAllStringSubmatch(value, -1) {
		_, password, found := strings.Cut(match[1], ":")
		if found && password != "" && literalValue(password, dev) {
			return true
		}
	}
	return false
}

func literalValue(value string, dev bool) bool {
	if !interpolation.MatchString(value) {
		return true
	}
	if !dev {
		for _, match := range secretDefault.FindAllStringSubmatch(value, -1) {
			if match[1] != "" {
				return true
			}
		}
	}
	return false
}
