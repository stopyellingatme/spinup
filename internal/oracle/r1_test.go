package oracle

import (
	"bytes"
	"testing"
)

// TestR1Fixtures requires every R1 family to participate in the frozen oracle.
func TestR1Fixtures(t *testing.T) {
	fixtures := generate(t)
	for _, name := range []string{"quotes-r1.json", "case-words.json", "semver.json", "compat.json", "when.json", "hashes.json", "clean-paths.json", "golden-modes.json"} {
		data, ok := fixtures[name]
		if !ok || len(bytes.TrimSpace(data)) == 0 {
			t.Errorf("missing R1 fixture family %s", name)
		}
	}
}

func generateR1(t *testing.T) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"quotes-r1.json":    jsonBytes(t, extraQuoteCases(t)),
		"case-words.json":   jsonBytes(t, wordsCases(t)),
		"semver.json":       jsonBytes(t, semverCases(t)),
		"compat.json":       jsonBytes(t, compatibilityCases()),
		"when.json":         jsonBytes(t, whenCases()),
		"hashes.json":       jsonBytes(t, hashCases(t)),
		"clean-paths.json":  jsonBytes(t, cleanPathCases()),
		"golden-modes.json": jsonBytes(t, goldenModesCases(t)),
	}
}
