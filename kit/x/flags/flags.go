package flags

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// MaxFlags is the largest number of declared flags (ADR 0029 decision 2.3).
const MaxFlags = 64

// keyPattern is the key rule of ADR 0029 decision 2.3.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// Decl is the declaration of one flag, from the generated file.
type Decl struct {
	Description string
	Default     bool
	Public      bool
	Expires     string // YYYY-MM-DD or empty
}

// Declared maps a key to its declaration. The generated Set is a Declared.
type Declared map[string]Decl

// Validate checks the count, the key rules, and the Expires format of d
// (ADR 0029 decision 2.3). It checks the keys in sorted order and returns
// the first failure. Each error ends with a Try: line.
func (d Declared) Validate() error {
	if len(d) > MaxFlags {
		const hint = "\nTry: remove [flags.<key>] tables from spinup.toml until 64 or fewer remain, and run spinup g flags."
		return fmt.Errorf("flags: %d flags are declared, and the limit is %d.%s", len(d), MaxFlags, hint)
	}
	for _, key := range d.Keys() {
		if err := validateKey(key); err != nil {
			return err
		}
		if exp := d[key].Expires; exp != "" {
			if _, err := time.Parse(time.DateOnly, exp); err != nil {
				return fmt.Errorf("flags: the flag %q has the expires value %q, which is not a YYYY-MM-DD date.%s", key, exp, tryKey(key))
			}
		}
	}
	return nil
}

// validateKey applies the key rules of ADR 0029 decision 2.3 to one key.
func validateKey(key string) error {
	switch {
	case !keyPattern.MatchString(key):
		return fmt.Errorf("flags: the key %q does not match %s.%s", key, keyPattern.String(), tryKey(key))
	case strings.Contains(key, "__"):
		return fmt.Errorf("flags: the key %q has a double underscore.%s", key, tryKey(key))
	case strings.HasSuffix(key, "_"):
		return fmt.Errorf("flags: the key %q has a trailing underscore.%s", key, tryKey(key))
	}
	return nil
}

// tryKey is the Try: line for an error in the declaration of key.
func tryKey(key string) string {
	return "\nTry: correct [flags." + key + "] in spinup.toml and run spinup g flags."
}

// Keys returns the declared keys in sorted order.
func (d Declared) Keys() []string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// clone returns a copy of d. A nil d gives an empty, non-nil map.
func (d Declared) clone() Declared {
	out := make(Declared, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// Expired reports whether the Expires date is before the date of now.
// The flag is not expired on its Expires date. An empty or invalid
// Expires is never expired.
func (d Decl) Expired(now time.Time) bool {
	if d.Expires == "" {
		return false
	}
	exp, err := time.Parse(time.DateOnly, d.Expires)
	if err != nil {
		return false
	}
	y, m, day := now.Date()
	return exp.Before(time.Date(y, m, day, 0, 0, 0, 0, time.UTC))
}

// Rule is the stored targeting of one flag.
type Rule struct {
	Enabled    bool
	Roles      []string       // sorted, unique
	Percentage int            // 0 to 100
	Users      map[int64]bool // user ID to on or off
}

// clone returns a deep copy of r with sorted, unique roles.
func (r Rule) clone() Rule {
	out := Rule{Enabled: r.Enabled, Percentage: r.Percentage}
	if r.Roles != nil {
		out.Roles = slices.Compact(slices.Sorted(slices.Values(r.Roles)))
	}
	if r.Users != nil {
		out.Users = make(map[int64]bool, len(r.Users))
		for id, on := range r.Users {
			out.Users[id] = on
		}
	}
	return out
}

// Snapshot is an immutable view of every rule at one time.
// It carries the declarations, so Resolve knows the declared keys and
// their defaults. Nothing changes a Snapshot after NewSnapshot.
type Snapshot struct {
	declared Declared
	rules    map[string]Rule
	loadedAt time.Time
}

// NewSnapshot returns a snapshot of rules for the declared flags.
// It copies the maps and the role slices, so later changes to the
// arguments do not change the snapshot. Roles are sorted and unique.
func NewSnapshot(declared Declared, rules map[string]Rule, loadedAt time.Time) *Snapshot {
	s := &Snapshot{
		declared: declared.clone(),
		rules:    make(map[string]Rule, len(rules)),
		loadedAt: loadedAt,
	}
	for k, r := range rules {
		s.rules[k] = r.clone()
	}
	return s
}

// Rule returns a copy of the rule of key. ok is false when the snapshot
// has no rule for key.
func (s *Snapshot) Rule(key string) (r Rule, ok bool) {
	if s == nil {
		return Rule{}, false
	}
	r, ok = s.rules[key]
	if !ok {
		return Rule{}, false
	}
	return r.clone(), true
}

// Keys returns the keys of the rules in sorted order. Stale keys (rules
// with no declaration) are included.
func (s *Snapshot) Keys() []string {
	if s == nil {
		return []string{}
	}
	keys := make([]string, 0, len(s.rules))
	for k := range s.rules {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// LoadedAt returns the time of the load. It is zero for an empty snapshot.
func (s *Snapshot) LoadedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.loadedAt
}

// Subject is who asks. Anonymous is true for a signed-out visitor.
type Subject struct {
	UserID    int64
	Role      string
	Anonymous bool
}

// Reason says which step decided the value (OpenFeature reasons).
type Reason string

// The reasons of ADR 0029 decision 5.
const (
	ReasonStatic         Reason = "static"          // the global switch decided
	ReasonDefault        Reason = "default"         // the declared default: no rule, or no snapshot
	ReasonTargetingMatch Reason = "targeting_match" // a user rule or a role rule
	ReasonSplit          Reason = "split"           // the percentage bucket
	ReasonError          Reason = "error"           // an undeclared key, or no flags scope in the context
)

// Resolution is the answer of one evaluation.
type Resolution struct {
	Value   bool
	Variant string // "on" or "off"
	Reason  Reason
}

// resolution builds a Resolution with the variant of value.
func resolution(value bool, reason Reason) Resolution {
	if value {
		return Resolution{Value: true, Variant: "on", Reason: reason}
	}
	return Resolution{Value: false, Variant: "off", Reason: reason}
}
