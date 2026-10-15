package maintain

import (
	"testing"
	"time"
)

func TestInWindow(t *testing.T) {
	t.Parallel()
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	// 2026-10-18 is a Sunday.
	for _, tc := range []struct {
		spec, now string
		want      bool
	}{
		{"", "2026-10-14T12:00:00Z", true},
		{"Sun 02:00-05:00", "2026-10-18T02:00:00Z", true},
		{"Sun 02:00-05:00", "2026-10-18T04:59:00Z", true},
		{"Sun 02:00-05:00", "2026-10-18T05:00:00Z", false},
		{"Sun 02:00-05:00", "2026-10-17T03:00:00Z", false},
		{"sun 02:00-05:00", "2026-10-18T05:00:00+02:00", true},
		{"Sun 02:00-05:00", "2026-10-18T03:00:00+02:00", false},
		{"Sat 23:00-02:00", "2026-10-17T23:30:00Z", true},
		{"Sat 23:00-02:00", "2026-10-18T01:59:00Z", true},
		{"Sat 23:00-02:00", "2026-10-18T23:30:00Z", false},
		{"Mon-Fri 22:00-23:00", "2026-10-16T22:10:00Z", true},
		{"Mon-Fri 22:00-23:00", "2026-10-17T22:10:00Z", false},
		{"Fri-Mon 01:00-02:00", "2026-10-19T01:30:00Z", true},
		{"Fri-Mon 01:00-02:00", "2026-10-20T01:30:00Z", false},
		{"* 03:00-04:00", "2026-10-14T03:30:00Z", true},
	} {
		got, err := InWindow(tc.spec, at(tc.now))
		if err != nil || got != tc.want {
			t.Errorf("InWindow(%q, %s) = %v, %v; want %v", tc.spec, tc.now, got, err, tc.want)
		}
	}
}

func TestParseWindowErrors(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{"Sun", "Sun 02:00", "Funday 02:00-03:00", "Sun 2:00-03:00", "Sun 24:00-01:00", "Sun 02:00-02:00", "Sun-Xyz 01:00-02:00"} {
		if _, err := ParseWindow(spec); err == nil {
			t.Errorf("ParseWindow(%q) accepted", spec)
		}
	}
}

func TestNext(t *testing.T) {
	t.Parallel()
	w, err := ParseWindow("Sun 02:00-05:00")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	if got, want := w.Next(now), time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
}
