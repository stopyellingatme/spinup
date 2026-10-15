package pack

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
)

var comparatorPattern = regexp.MustCompile(`^(>=|<=|>|<|=|\^|~)?(v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)$`)
var comparatorSpace = regexp.MustCompile(`(>=|<=|>|<|=|\^|~)\s+`)

// matchesRange supports comparator conjunctions, || alternatives, ^, ~, and *.
// Development builds validate the range syntax but accept any valid range.
func matchesRange(expression, version string) (bool, error) {
	if strings.TrimSpace(expression) == "" {
		return false, fmt.Errorf("spinup range is required")
	}
	dev := version == "dev" || version == "(devel)" || version == ""
	current, err := parseVersion(version)
	if err != nil && !dev {
		return false, err
	}
	anyMatch := false
	for _, alternative := range strings.Split(expression, "||") {
		terms := strings.Fields(comparatorSpace.ReplaceAllString(strings.ReplaceAll(alternative, ",", " "), "$1"))
		if len(terms) == 0 {
			return false, fmt.Errorf("invalid spinup range %q", expression)
		}
		match := true
		for _, term := range terms {
			if term == "*" {
				continue
			}
			parts := comparatorPattern.FindStringSubmatch(term)
			if parts == nil {
				return false, fmt.Errorf("invalid spinup range term %q", term)
			}
			target, err := parseVersion(parts[2])
			if err != nil {
				return false, err
			}
			c := current.Compare(target)
			switch parts[1] {
			case "", "=":
				match = match && c == 0
			case ">=":
				match = match && c >= 0
			case "<=":
				match = match && c <= 0
			case ">":
				match = match && c > 0
			case "<":
				match = match && c < 0
			case "~", "^":
				upper := target
				upper.Pre = ""
				switch {
				case parts[1] == "~":
					upper.Minor++
					upper.Patch = 0
				case target.Major > 0:
					upper.Major++
					upper.Minor = 0
					upper.Patch = 0
				case target.Minor > 0:
					upper.Minor++
					upper.Patch = 0
				default:
					upper.Patch++
				}
				match = match && c >= 0 && current.Compare(upper) < 0
			}
		}
		anyMatch = anyMatch || match
	}
	return dev || anyMatch, nil
}
func parseVersion(value string) (compat.Version, error) {
	core, _, _ := strings.Cut(value, "+")
	return compat.ParseVersion(core)
}
