package render

import "testing"

func TestHasAcceptsLockAnswers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		values any
		want   bool
	}{
		{[]string{"ci", "auth"}, true},
		{[]any{"ci", "auth"}, true},
		{[]any{"ci"}, false},
		{nil, false},
	} {
		got, err := has("auth", tc.values)
		if err != nil || got != tc.want {
			t.Errorf("has(auth, %#v) = %v, %v; want %v", tc.values, got, err, tc.want)
		}
	}
	if _, err := has("auth", "auth"); err == nil {
		t.Error("has accepted a string")
	}
}
