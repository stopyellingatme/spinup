package maintain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Contract file from the M7 pre-seed (ADR 0011 §6). Main loop only: units do not edit it.

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// Window is a parsed maintenance window. Times are UTC.
type Window struct {
	// Days holds the days on which the window starts. All seven when the spec is "*".
	Days [7]bool
	// Start and End are minutes after midnight. End <= Start means the window crosses midnight.
	Start, End int
}

// ParseWindow parses "<Day[-Day]|*> HH:MM-HH:MM", such as "Sun 02:00-05:00", "Mon-Fri 22:00-02:00",
// or "* 03:00-04:00". Day names are the first three letters, in any case.
func ParseWindow(spec string) (Window, error) {
	var w Window
	fields := strings.Fields(spec)
	if len(fields) != 2 {
		return w, windowError(spec, "want a day part and a time part")
	}
	if fields[0] == "*" {
		for i := range w.Days {
			w.Days[i] = true
		}
	} else {
		from, to, ranged := strings.Cut(strings.ToLower(fields[0]), "-")
		first, ok := weekdays[from]
		if !ok {
			return w, windowError(spec, fmt.Sprintf("unknown day %q", from))
		}
		last := first
		if ranged {
			if last, ok = weekdays[to]; !ok {
				return w, windowError(spec, fmt.Sprintf("unknown day %q", to))
			}
		}
		for d := first; ; d = (d + 1) % 7 {
			w.Days[d] = true
			if d == last {
				break
			}
		}
	}
	start, end, ok := strings.Cut(fields[1], "-")
	if !ok {
		return w, windowError(spec, "want a time range HH:MM-HH:MM")
	}
	var err error
	if w.Start, err = minutes(start); err != nil {
		return w, windowError(spec, err.Error())
	}
	if w.End, err = minutes(end); err != nil {
		return w, windowError(spec, err.Error())
	}
	if w.Start == w.End {
		return w, windowError(spec, "the window has no length")
	}
	return w, nil
}

// Contains reports whether t (converted to UTC) is inside the window. A window that crosses
// midnight belongs to the day on which it starts.
func (w Window) Contains(t time.Time) bool {
	t = t.UTC()
	m := t.Hour()*60 + t.Minute()
	if w.End > w.Start {
		return w.Days[t.Weekday()] && m >= w.Start && m < w.End
	}
	if m >= w.Start {
		return w.Days[t.Weekday()]
	}
	return m < w.End && w.Days[(t.Weekday()+6)%7]
}

// Next returns the next start of the window at or after t, in UTC.
func (w Window) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		d := day.AddDate(0, 0, i)
		start := d.Add(time.Duration(w.Start) * time.Minute)
		if w.Days[d.Weekday()] && !start.Before(t) {
			return start
		}
	}
	return time.Time{}
}

// InWindow reports whether now is inside the window spec. An empty spec means any time.
func InWindow(spec string, now time.Time) (bool, error) {
	if strings.TrimSpace(spec) == "" {
		return true, nil
	}
	w, err := ParseWindow(spec)
	if err != nil {
		return false, err
	}
	return w.Contains(now), nil
}

func minutes(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, fmt.Errorf("time %q is not HH:MM", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("time %q is not HH:MM", s)
	}
	return hh*60 + mm, nil
}

func windowError(spec, why string) error {
	return fmt.Errorf("invalid maintenance window %q: %s\nTry: set [maintenance] window in spinup.toml to a value such as \"Sun 02:00-05:00\" (UTC)", spec, why)
}
