package apikeys

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Plan is the quota of one plan. All keys of a user share it.
type Plan struct {
	// PerMinute is the request limit for each minute, in each process.
	PerMinute int
	// PerDay is the request limit for each UTC day, shared through Postgres.
	PerDay int
}

// Config is the [apikeys] table of spinup.toml, as spinup g apikeys writes it.
type Config struct {
	// Prefix starts each token: <prefix>_<secret>.
	Prefix string
	// DefaultPlan is the plan of a user with no plan row.
	DefaultPlan string
	// MaxKeys is the count of keys that are not revoked, for each user.
	MaxKeys int
	// Plans maps a plan name to its quota.
	Plans map[string]Plan
}

// Scope is what a key can do.
type Scope string

// The scopes of a key.
const (
	// ScopeRead allows GET and HEAD requests only.
	ScopeRead Scope = "read"
	// ScopeWrite allows all methods.
	ScopeWrite Scope = "write"
)

// Valid reports whether s is ScopeRead or ScopeWrite.
func (s Scope) Valid() bool { return s == ScopeRead || s == ScopeWrite }

var (
	prefixPattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)
	planPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

const configHint = "\nTry: correct [apikeys] in spinup.toml and run spinup g apikeys."

// PlanNames returns the plan names in sorted order.
func (c Config) PlanNames() []string {
	names := make([]string, 0, len(c.Plans))
	for name := range c.Plans {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Validate checks the rules of ADR 0030 decision 2.3.
func (c Config) Validate() error {
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf("apikeys: "+format+configHint, args...))
	}
	if !prefixPattern.MatchString(c.Prefix) {
		fail("the prefix %q does not match %s", c.Prefix, prefixPattern.String())
	}
	if c.MaxKeys < 1 || c.MaxKeys > 100 {
		fail("MaxKeys %d must be 1 to 100", c.MaxKeys)
	}
	if len(c.Plans) < 1 || len(c.Plans) > 16 {
		fail("the plan count %d must be 1 to 16", len(c.Plans))
	}
	for _, name := range c.PlanNames() {
		plan := c.Plans[name]
		switch {
		case !planPattern.MatchString(name):
			fail("the plan name %q does not match %s", name, planPattern.String())
		case plan.PerMinute < 1 || plan.PerMinute > 100000:
			fail("plan %q: PerMinute %d must be 1 to 100000", name, plan.PerMinute)
		case plan.PerDay < 1 || plan.PerDay > 100000000:
			fail("plan %q: PerDay %d must be 1 to 100000000", name, plan.PerDay)
		case plan.PerDay < plan.PerMinute:
			fail("plan %q: PerDay %d must be at least PerMinute %d", name, plan.PerDay, plan.PerMinute)
		}
	}
	if _, ok := c.Plans[c.DefaultPlan]; !ok {
		fail("DefaultPlan %q is not a declared plan (declared: %s)", c.DefaultPlan, strings.Join(c.PlanNames(), ", "))
	}
	return errors.Join(problems...)
}
