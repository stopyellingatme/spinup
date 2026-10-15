package compat

import (
	"strings"
	"testing"
)

func TestParseVersionAndCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  string
	}{
		{"1.2.3", "v1.2.3"},
		{"v1.2.3-alpha.1", "v1.2.3-alpha.1"},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			t.Parallel()
			version, err := ParseVersion(test.value)
			if err != nil || version.String() != test.want {
				t.Fatalf("ParseVersion(%q) = %v, %v; want %q", test.value, version, err, test.want)
			}
		})
	}

	ordered := []string{
		"v1.0.0-alpha",
		"v1.0.0-alpha.1",
		"v1.0.0-alpha.beta",
		"v1.0.0-beta",
		"v1.0.0-beta.2",
		"v1.0.0-beta.11",
		"v1.0.0-rc.1",
		"v1.0.0",
	}
	for index := 1; index < len(ordered); index++ {
		previous, _ := ParseVersion(ordered[index-1])
		current, _ := ParseVersion(ordered[index])
		if previous.Compare(current) >= 0 {
			t.Errorf("%s must sort before %s", previous, current)
		}
	}
	for _, invalid := range []string{"1.2", "v01.2.3", "1.2.3-01", "1.2.3+build"} {
		if _, err := ParseVersion(invalid); err == nil {
			t.Errorf("ParseVersion(%q) succeeded", invalid)
		}
	}
}

func TestCheckKit(t *testing.T) {
	t.Parallel()

	if err := CheckKit("v1.2.3", "v1.2.99"); err != nil {
		t.Fatalf("CheckKit() same minor error = %v", err)
	}
	if err := CheckKit("dev", "bad"); err != nil {
		t.Fatalf("CheckKit() dev error = %v", err)
	}
	err := CheckKit("v1.2.3", "v1.3.0")
	if err == nil || !strings.Contains(err.Error(), "Try:") || !strings.Contains(err.Error(), "v1.2.x") {
		t.Fatalf("CheckKit() mismatch error = %v", err)
	}
}

func TestSupportedTemplateVersions(t *testing.T) {
	t.Parallel()

	got := SupportedTemplateVersions("v1.4.0", []string{"v1.5.0", "v1.0.0", "v1.4.0", "v1.3.0", "v1.2.0", "bad"})
	want := []string{"v1.4.0", "v1.3.0", "v1.2.0"}
	if len(got) != len(want) {
		t.Fatalf("SupportedTemplateVersions() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("SupportedTemplateVersions() = %v, want %v", got, want)
		}
	}
}

func TestCheckMinCLI(t *testing.T) {
	t.Parallel()

	if err := CheckMinCLI("dev", "v99.0.0"); err != nil {
		t.Fatalf("CheckMinCLI() dev error = %v", err)
	}
	if err := CheckMinCLI("v1.2.3", "v1.2.3"); err != nil {
		t.Fatalf("CheckMinCLI() equal error = %v", err)
	}
	err := CheckMinCLI("v1.2.3", "v1.2.4")
	if err == nil || !strings.Contains(err.Error(), "Try: spinup self-update") {
		t.Fatalf("CheckMinCLI() error = %v", err)
	}
}
