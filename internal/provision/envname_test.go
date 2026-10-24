package provision

import "testing"

// DigitalOcean refuses a droplet name that ends with a dash, and the
// environment name ends the droplet name.
func TestEnvNamePattern(t *testing.T) {
	for _, ok := range []string{"production", "staging-2", "a", "e1"} {
		if !envNamePattern.MatchString(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"prod-", "-prod", "Prod", "1prod", "", "a_b"} {
		if envNamePattern.MatchString(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}
