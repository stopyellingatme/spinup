package apicompat

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var depSuffix = regexp.MustCompile(` deprecated:v0\.(\d+)$`)

// split returns the key (package, kind, and name), the signature without the
// deprecation marker, and the deprecation minor (-1 if none).
func split(line string) (key, body string, dep int) {
	dep = -1
	body = line
	if m := depSuffix.FindStringSubmatch(line); m != nil {
		dep, _ = strconv.Atoi(m[1])
		body = strings.TrimSuffix(line, m[0])
	}
	parts := strings.SplitN(body, " ", 3)
	if len(parts) < 3 {
		return body, body, dep
	}
	rest := parts[2]
	end := strings.IndexAny(rest, "([ ")
	if end < 0 {
		end = len(rest)
	}
	return parts[0] + " " + parts[1] + " " + rest[:end], body, dep
}

// Diff is the difference between a baseline and the current API.
type Diff struct {
	// Removed lists baseline lines that are gone and not allowed to go.
	Removed []string
	// Changed lists "old => new" pairs with the same identifier.
	Changed []string
	// Added lists new lines.
	Added []string
	// Marked lists lines whose only change is the deprecation marker.
	Marked []string
	// Allowed lists removals of deprecated API that is old enough.
	Allowed []string
}

// Breaking reports whether the diff has a removal or a change.
func (d Diff) Breaking() bool { return len(d.Removed)+len(d.Changed) > 0 }

// Clean reports whether the baseline needs no update.
func (d Diff) Clean() bool {
	return !d.Breaking() && len(d.Added)+len(d.Marked)+len(d.Allowed) == 0
}

// Compare compares the baseline with the current API. A removal is allowed
// when the baseline line, or the type that owns it, carries a deprecation
// marker for a minor that is at least one minor older than minor.
func Compare(baseline, current []string, minor int) Diff {
	cur := map[string]string{}
	for _, line := range current {
		key, _, _ := split(line)
		cur[key] = line
	}
	base := map[string]string{}
	oldDeprecated := map[string]bool{}
	for _, line := range baseline {
		key, _, dep := split(line)
		base[key] = line
		if dep >= 0 && dep < minor {
			oldDeprecated[key] = true
		}
	}
	var d Diff
	for _, line := range baseline {
		key, body, _ := split(line)
		now, ok := cur[key]
		if !ok {
			if oldDeprecated[key] || oldDeprecated[ownerKey(key)] {
				d.Allowed = append(d.Allowed, line)
			} else {
				d.Removed = append(d.Removed, line)
			}
			continue
		}
		_, nowBody, _ := split(now)
		switch {
		case nowBody != body:
			d.Changed = append(d.Changed, line+"  =>  "+now)
		case now != line:
			d.Marked = append(d.Marked, line+"  =>  "+now)
		}
	}
	for _, line := range current {
		if key, _, _ := split(line); base[key] == "" {
			d.Added = append(d.Added, line)
		}
	}
	return d
}

// ownerKey returns the key of the type that owns a field or method key, or "".
func ownerKey(key string) string {
	parts := strings.SplitN(key, " ", 3)
	if len(parts) < 3 || !strings.Contains(parts[2], ".") {
		return ""
	}
	owner, _, _ := strings.Cut(parts[2], ".")
	return parts[0] + " type " + owner
}

// Message formats a diff as a test failure message.
func (d Diff) Message() string {
	var b strings.Builder
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		b.WriteString(title + ":\n")
		for _, line := range lines {
			b.WriteString("  " + line + "\n")
		}
	}
	section("removed from the kit API (breaking)", d.Removed)
	section("changed in the kit API (breaking)", d.Changed)
	section("added to the kit API", d.Added)
	section("deprecation marker changed", d.Marked)
	section("removed after deprecation (allowed)", d.Allowed)
	if d.Breaking() {
		b.WriteString("Try: restore the removed or changed API, or deprecate it with `Deprecated: since v0.N. Use X instead.` and remove it one minor later.\n")
	} else {
		b.WriteString("Try: if the change is intended, run `SPINUP_GOLDEN_UPDATE=1 go test ./internal/apicompat` and commit kit/api/current.txt.\n")
	}
	return b.String()
}

// ReadLines reads a baseline file. A missing file returns no lines.
func ReadLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// ReadMinor reads the current kit minor from a file that holds "0.N".
func ReadMinor(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read kit minor: %w\nTry: create %s with the current development version, for example 0.1", err, path)
	}
	text := strings.TrimSpace(string(data))
	text = strings.TrimPrefix(text, "v")
	rest, ok := strings.CutPrefix(text, "0.")
	if !ok {
		return 0, fmt.Errorf("kit minor %q in %s is not 0.N\nTry: write the minor as 0.N", text, path)
	}
	minor, err := strconv.Atoi(rest)
	if err != nil {
		return 0, fmt.Errorf("kit minor %q in %s is not 0.N: %w\nTry: write the minor as 0.N", text, path, err)
	}
	return minor, nil
}
