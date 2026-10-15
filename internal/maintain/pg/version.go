package pg

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/stopyellingatme/spinup/internal/db/ops"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

type version struct{ major, minor int }

var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)(?:\s|[-+.]|$)`)

func parseVersion(s string) (version, error) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return version{}, fmt.Errorf("read PostgreSQL version: invalid version %q\nTry: inspect SHOW server_version on the VM", s)
	}
	major, e1 := strconv.Atoi(m[1])
	minor, e2 := strconv.Atoi(m[2])
	if e1 != nil || e2 != nil {
		return version{}, fmt.Errorf("read PostgreSQL version: version exceeds integer range\nTry: inspect SHOW server_version on the VM")
	}
	return version{major, minor}, nil
}
func pinnedImage(major int) (stackversions.Image, error) {
	key := "postgres"
	switch major {
	case 16, 17:
		key = fmt.Sprintf("postgres-%d", major)
	case 18:
	default:
		return stackversions.Image{}, fmt.Errorf("select PostgreSQL image: major %d has no supported pin\nTry: select 16, 17, or 18", major)
	}
	return stackversions.MustLoad().Image(key)
}
func targetImage(from, to int) (stackversions.Image, error) {
	check, err := ops.CheckServerVersion(strconv.Itoa(to) + "0000")
	if err != nil || check.Fail != "" {
		return stackversions.Image{}, fmt.Errorf("select PostgreSQL major: %d is unsupported by db doctor\nTry: select a supported major, 16, 17, or 18", to)
	}
	if to <= from {
		return stackversions.Image{}, fmt.Errorf("upgrade PostgreSQL: target %d must exceed current major %d\nTry: select a newer major with --to", to, from)
	}
	return pinnedImage(to)
}
