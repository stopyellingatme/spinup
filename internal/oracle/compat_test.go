package oracle

import (
	"testing"

	"github.com/stopyellingatme/spinup/internal/compat"
)

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

type versionCase struct {
	Input     string `json:"input"`
	Canonical string `json:"canonical"`
	Major     int    `json:"major"`
	Minor     int    `json:"minor"`
	Patch     int    `json:"patch"`
	Pre       string `json:"pre"`
	Error     string `json:"error"`
}

type compareCase struct {
	Left     string `json:"left"`
	Right    string `json:"right"`
	Expected int    `json:"expected"`
}

func semverCases(t *testing.T) any {
	t.Helper()
	inputs := []string{"0.0.0", "v1.2.3", "1.2.3-alpha.1", "1.2.3-0", "1.2.3-a-01", "1.2.3-9999999999999999999999999999999999999999", "9223372036854775807.0.0", "", "dev", "1", "1.2", "1.2.3.4", "vv1.2.3", "V1.2.3", "01.2.3", "1.02.3", "1.2.03", "1..3", "-1.2.3", "+1.2.3", " 1.2.3", "1.2.3\n", "1.2.3+build", "1.2.3-", "1.2.3-01", "1.2.3-a..b", "1.2.3-a_b", "1.2.3-é", "1.2.3-rc.1+meta", "9223372036854775808.0.0"}
	parse := make([]versionCase, 0, len(inputs))
	for _, input := range inputs {
		v, err := compat.ParseVersion(input)
		item := versionCase{Input: input, Error: errorText(err)}
		if err == nil {
			item.Canonical, item.Major, item.Minor, item.Patch, item.Pre = v.String(), v.Major, v.Minor, v.Patch, v.Pre
		}
		parse = append(parse, item)
	}
	ordered := []string{"0.9.9", "1.0.0-0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0-rc.999999999999999999999999999999", "1.0.0", "v1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	comparisons := make([]compareCase, 0, len(ordered)*len(ordered))
	for _, left := range ordered {
		a, err := compat.ParseVersion(left)
		if err != nil {
			t.Fatal(err)
		}
		for _, right := range ordered {
			b, err := compat.ParseVersion(right)
			if err != nil {
				t.Fatal(err)
			}
			comparisons = append(comparisons, compareCase{left, right, a.Compare(b)})
		}
	}
	return struct {
		Parse   []versionCase `json:"parse"`
		Compare []compareCase `json:"compare"`
	}{parse, comparisons}
}

type compatibilityCase struct {
	CLI   string `json:"cli"`
	Other string `json:"other"`
	Error string `json:"error"`
}

type supportedCase struct {
	CLI      string   `json:"cli"`
	Released []string `json:"released"`
	Expected []string `json:"expected"`
}

func compatibilityCases() any {
	pairs := [][2]string{{"dev", "bad"}, {"bad", "v1.2.3"}, {"v1.2.3", "bad"}, {"v1.2.3", "v1.2.99"}, {"v1.2.3-alpha", "1.2.3-rc.1"}, {"v1.2.3", "v1.3.0"}, {"v1.2.3", "v2.2.3"}, {"1.2.3", "v1.2.3"}, {"v1.2.3", "1.2.2"}, {"v1.2.3", "1.2.4"}, {"v1.2.3-alpha", "1.2.3"}, {"v1.2.3", "1.2.3-alpha"}}
	kit, minimum := []compatibilityCase{}, []compatibilityCase{}
	for _, pair := range pairs {
		kit = append(kit, compatibilityCase{pair[0], pair[1], errorText(compat.CheckKit(pair[0], pair[1]))})
		minimum = append(minimum, compatibilityCase{pair[0], pair[1], errorText(compat.CheckMinCLI(pair[0], pair[1]))})
	}
	supported := []supportedCase{}
	for _, cli := range []string{"dev", "bad", "v0.0.0", "v1.2.3-alpha", "v1.2.3", "v2.0.0"} {
		for _, released := range [][]string{nil, {}, {"bad", "v1.2.3+build"}, {"v2.0.0", "v1.2.4", "v1.2.3", "1.2.3", "v1.2.3-alpha", "v1.2.2", "v1.1.0", "bad"}, {"v1.0.0", "v1.0.0", "1.0.0", "v0.9.0"}} {
			supported = append(supported, supportedCase{cli, released, compat.SupportedTemplateVersions(cli, released)})
		}
	}
	return struct {
		Kit       []compatibilityCase `json:"kit"`
		MinCLI    []compatibilityCase `json:"min_cli"`
		Supported []supportedCase     `json:"supported"`
	}{kit, minimum, supported}
}
