package oracle

import (
	"testing"

	"github.com/stopyellingatme/spinup/internal/templates"
)

type whenCase struct {
	Expression string         `json:"expression"`
	Answers    map[string]any `json:"answers"`
	Expected   bool           `json:"expected"`
	Error      string         `json:"error"`
}

func whenCases() []whenCase {
	cases := []whenCase{}
	expressions := []string{"", " \t\n", "on", "!on", "! off", "on && off || on", "on || off && off", "off && on || off", "kind == 'api'", "kind != 'web'", "empty == ''", "modules contains 'auth'", "modules contains 'missing'", "on || missing", "off && missing", "missing", "kind", "!kind", "on == 'true'", "kind contains 'api'", "modules == 'auth'", "(on)", "on & off", "on | off", "on &&", "on ||", "!!on", "!kind == 'api'", "kind == \"api\"", "kind == 'unterminated", "modules contains'auth'", "kind == 'a'junk", "true", "1key", "_key1", "kind == 'a\\b'"}
	answers := map[string]any{"on": true, "off": false, "kind": "api", "empty": "", "modules": []string{"auth", "jobs"}, "_key1": true}
	for _, expression := range expressions {
		cases = append(cases, evaluateWhen(expression, answers))
	}
	for _, value := range []any{nil, true, false, "", "api", 0, []string{}, []string{"auth"}, []any{}, []any{"auth", false, nil}, map[string]any{}} {
		for _, expression := range []string{"value", "!value", "value == 'api'", "value != ''", "value contains 'auth'"} {
			cases = append(cases, evaluateWhen(expression, map[string]any{"value": value}))
		}
	}
	cases = append(cases, evaluateWhen("", nil), evaluateWhen("missing", nil))
	return cases
}

func evaluateWhen(expression string, answers map[string]any) whenCase {
	result, err := (templates.FileSpec{When: expression}).Matches(answers)
	return whenCase{expression, answers, result, errorText(err)}
}

// Both list representations must give the same result after JSON decoding.
func TestWhenListRepresentations(t *testing.T) {
	for _, expression := range []string{"modules contains 'auth'", "modules contains 'jobs'"} {
		validated := evaluateWhen(expression, map[string]any{"modules": []string{"auth"}})
		decoded := evaluateWhen(expression, map[string]any{"modules": []any{"auth"}})
		if validated.Expected != decoded.Expected || validated.Error != decoded.Error {
			t.Fatalf("list representation changes %q", expression)
		}
	}
}
