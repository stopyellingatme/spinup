package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input string
		want        map[string]string
		bad         bool
	}{
		{"syntax", "# comment\nA=one # tail\nexport B='two # literal'\nC=\"three\\nlines\" # tail\nD=\nE=a=b\n", map[string]string{"A": "one", "B": "two # literal", "C": "three\nlines", "D": "", "E": "a=b"}, false},
		{"malformed", "NO_EQUALS\nA='unterminated\n1INVALID=x\n", nil, true},
		{"quote tail", "A='value' garbage", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseDotenv(strings.NewReader(tc.input))
			if (err != nil) != tc.bad {
				t.Fatalf("%v", err)
			}
			if !tc.bad && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%v", got)
			}
		})
	}
}
