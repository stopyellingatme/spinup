package live

import (
	"strings"
	"testing"
)

func TestCursor(t *testing.T) {
	for _, tt := range []struct {
		s    string
		want Cursor
	}{
		{"0-0", Cursor{}}, {"1731500000000-42", Cursor{1731500000000, 42}},
		{"0001-0002", Cursor{1, 2}}, {"999999999999999999-999999999999999999", Cursor{999999999999999999, 999999999999999999}},
	} {
		got, err := ParseCursor(tt.s)
		if err != nil || got != tt.want {
			t.Errorf("%q: %+v %v", tt.s, got, err)
		}
		round, err := ParseCursor(got.String())
		if err != nil || round != got {
			t.Errorf("round trip: %+v %v", round, err)
		}
	}
	for _, s := range []string{"", "1", "-1", "1-", "-1-2", "+1-2", "1-+2", "1--2", "1-2-3", "1 -2", "1- 2", "1-2\n", "a-2", "1-9223372036854775808", "1234567890123456789-1", "1-1234567890123456789"} {
		if _, err := ParseCursor(s); err == nil || !strings.Contains(err.Error(), "\nTry:") {
			t.Errorf("%q: %v", s, err)
		}
	}
	if got := (Cursor{1731500000000, 42}).String(); got != "1731500000000-42" {
		t.Fatal(got)
	}
	if n := testing.AllocsPerRun(100, func() { cursorStringSink = (Cursor{1731500000000, 42}).String() }); n > 1 {
		t.Fatalf("String allocations: %g", n)
	}
}

var cursorStringSink string
