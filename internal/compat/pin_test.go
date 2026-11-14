package compat

import "testing"

func TestPinState(t *testing.T) {
	t.Parallel()
	older := "spinup 0.1.0-alpha.700 is older than 0.1.0-alpha.830, the version that wrote spinup.lock"
	newer := "spinup 0.1.0-alpha.830 is newer than 0.1.0-alpha.700, the version that wrote spinup.lock"
	for _, tc := range []struct {
		name, cli, pin, message, try string
		state                        PinState
	}{
		{"CLI development", "dev", "0.1.0", "", "", PinUnknown},
		{"pin development", "0.1.0", "dev", "", "", PinUnknown},
		{"empty pin", "0.1.0", "", "", "", PinUnknown},
		{"pseudo version", "0.1.0", "v0.0.0-20261114000000-abcdef", "", "", PinUnknown},
		{"pseudo version without a tag", "0.1.0-alpha.900", "v0.0.0-20261114000000-abcdef123456", "", "", PinUnknown},
		{"pseudo version after a prerelease tag", "0.1.0-alpha.900", "v0.1.0-alpha.830.0.20261114000000-abcdef123456", "", "", PinUnknown},
		{"pseudo version after a release tag", "0.1.0", "v0.1.1-0.20261114000000-abcdef123456", "", "", PinUnknown},
		{"CLI pseudo version after a prerelease tag", "v0.1.0-alpha.830.0.20261114000000-abcdef123456", "0.1.0-alpha.700", "", "", PinUnknown},
		{"CLI pseudo version after a release tag", "v0.2.1-0.20261114000000-abcdef123456", "0.1.0", "", "", PinUnknown},
		{"CLI pseudo version without a tag", "v0.0.0-20261114000000-abcdef123456", "0.1.0", "", "", PinUnknown},
		{"timestamp-like prerelease counter", "0.1.0-alpha.20261114000000", "0.1.0-alpha.20261114000000", "", "", PinMatch},
		{"equal", "v0.1.0-alpha.830", "0.1.0-alpha.830", "", "", PinMatch},
		{"older prerelease", "0.1.0-alpha.700", "0.1.0-alpha.830", older, "spinup self-update", PinCLIOlder},
		{"newer prerelease", "0.1.0-alpha.830", "0.1.0-alpha.700", newer, "spinup upgrade --plan", PinCLINewer},
		{"gap nineteen", "0.1.0-alpha.819", "0.1.0-alpha.800", "", "", PinMatch},
		{"gap twenty", "0.1.0-alpha.820", "0.1.0-alpha.800", "spinup 0.1.0-alpha.820 is newer than 0.1.0-alpha.800, the version that wrote spinup.lock", "spinup upgrade --plan", PinCLINewer},
		{"different labels", "0.1.0-alpha.5", "0.1.0-beta.1", "spinup 0.1.0-alpha.5 is older than 0.1.0-beta.1, the version that wrote spinup.lock", "spinup self-update", PinCLIOlder},
		{"minor", "0.2.0", "0.1.9", "spinup 0.2.0 is newer than 0.1.9, the version that wrote spinup.lock", "spinup upgrade --plan", PinCLINewer},
		{"major", "1.0.0", "0.9.0", "spinup 1.0.0 is newer than 0.9.0, the version that wrote spinup.lock", "spinup upgrade --plan", PinCLINewer},
		{"patch", "0.1.0", "0.1.3", "", "", PinMatch},
		{"release and prerelease", "0.1.0", "0.1.0-alpha.830", "", "", PinMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := CheckPin(tc.cli, tc.pin)
			if got.State != tc.state || got.Message != tc.message || got.Try != tc.try {
				t.Fatalf("CheckPin(%q, %q) = %+v", tc.cli, tc.pin, got)
			}
		})
	}
}
