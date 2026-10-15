package pg

import (
	"fmt"
	"testing"
)

func TestVersion(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		value        string
		major, minor int
		valid        bool
	}{
		{"17.10 (Debian 17.10-1)", 17, 10, true}, {"18.6-trixie", 18, 6, true},
		{"16.15", 16, 15, true}, {"18beta1", 0, 0, false}, {"garbage", 0, 0, false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			v, err := parseVersion(tt.value)
			if (err == nil) != tt.valid || v.major != tt.major || v.minor != tt.minor {
				t.Fatalf("got %+v, %v", v, err)
			}
		})
	}
}

func TestTarget(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		from, to int
		valid    bool
	}{{16, 17, true}, {17, 18, true}, {18, 17, false}, {17, 17, false}, {16, 19, false}, {14, 15, false}, {15, 16, true}} {
		t.Run(fmt.Sprintf("%d-to-%d", tt.from, tt.to), func(t *testing.T) {
			t.Parallel()
			_, err := targetImage(tt.from, tt.to)
			if (err == nil) != tt.valid {
				t.Fatalf("%d -> %d: %v", tt.from, tt.to, err)
			}
		})
	}
}
