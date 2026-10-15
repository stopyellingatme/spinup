package templates

import "testing"

func TestWhenContainsAcceptsLockAnswers(t *testing.T) {
	t.Parallel()
	f := FileSpec{When: "modules contains 'auth'"}
	for _, tc := range []struct {
		modules any
		want    bool
	}{
		{[]string{"ci", "auth"}, true},
		{[]any{"ci", "auth"}, true},
		{[]any{"ci"}, false},
	} {
		got, err := f.Matches(map[string]any{"modules": tc.modules})
		if err != nil || got != tc.want {
			t.Errorf("Matches(%#v) = %v, %v; want %v", tc.modules, got, err, tc.want)
		}
	}
}
