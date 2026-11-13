package httpx

import "testing"

func TestAcceptsEncoding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, header, coding string
		want                 bool
	}{
		{"empty", "", "gzip", false},
		{"direct", "gzip", "gzip", true},
		{"spaces and case", " br , GZip ; q=0.5 ", "gzip", true},
		{"refused", "gzip; q=0", "gzip", false},
		{"wildcard", "br, *; q=0.5", "gzip", true},
		{"wildcard refused", "*; q=0", "gzip", false},
		{"named refusal overrides wildcard", "*, gzip; q=0", "gzip", false},
		{"unrelated", "br", "gzip", false},
		{"invalid quality", "gzip; q=wat", "gzip", false},
		{"not a number quality", "gzip; q=NaN", "gzip", false},
		{"out of range quality", "gzip; q=1.1", "gzip", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := AcceptsEncoding(tc.header, tc.coding); got != tc.want {
				t.Errorf("AcceptsEncoding(%q, %q) = %t, want %t", tc.header, tc.coding, got, tc.want)
			}
		})
	}
}
