package doctor

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Scope identifies where a check runs.
type Scope int

const (
	// Local identifies checks that run on the local machine.
	Local Scope = iota
	// Remote identifies checks that run against a remote environment.
	Remote
)

// Status identifies a check result state.
type Status int

const (
	// OK identifies a successful check.
	OK Status = iota
	// Warn identifies a successful check with a warning.
	Warn
	// Fail identifies a failed check.
	Fail
	// Skip identifies a check that did not run.
	Skip
)

// String returns the text representation of a status.
func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	case Skip:
		return "skip"
	default:
		return "unknown"
	}
}

// Result holds the result of a doctor check.
type Result struct {
	Status  Status
	Message string
	Try     string
}

// Context holds shared doctor check values.
type Context struct {
	Root  string
	Env   string
	Quick bool
	Deps  map[string]any
}

// Check defines a registered doctor check.
type Check struct {
	ID    string
	Title string
	Scope Scope
	Quick bool
	Run   func(ctx context.Context, c *Context) Result
}

// Report associates a check with its result.
type Report struct {
	Check  Check
	Result Result
}

var registry struct {
	sync.Mutex
	checks map[string]Check
}

// Register adds a doctor check to the registry.
func Register(check Check) {
	if check.ID == "" {
		panic("doctor: register check with empty ID")
	}
	if check.Run == nil {
		panic("doctor: register check with nil Run")
	}
	registry.Lock()
	defer registry.Unlock()
	if registry.checks == nil {
		registry.checks = make(map[string]Check)
	}
	if _, exists := registry.checks[check.ID]; exists {
		panic(fmt.Sprintf("doctor: duplicate check ID %q", check.ID))
	}
	registry.checks[check.ID] = check
}

// Checks returns checks for a scope, sorted by ID.
func Checks(scope Scope, quickOnly bool) []Check {
	registry.Lock()
	defer registry.Unlock()
	checks := make([]Check, 0, len(registry.checks))
	for _, check := range registry.checks {
		if check.Scope == scope && (!quickOnly || check.Quick) {
			checks = append(checks, check)
		}
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	return checks
}

// RunAll runs all checks for a scope in ID order.
func RunAll(ctx context.Context, scope Scope, c *Context) []Report {
	checks := Checks(scope, false)
	reports := make([]Report, 0, len(checks))
	for _, check := range checks {
		reports = append(reports, Report{Check: check, Result: run(ctx, check, c)})
	}
	return reports
}

func run(ctx context.Context, check Check, c *Context) (result Result) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = Result{Status: Fail, Message: fmt.Sprintf("check %q panicked: %v", check.ID, recovered)}
		}
	}()
	return check.Run(ctx, c)
}

func reset() {
	registry.Lock()
	defer registry.Unlock()
	registry.checks = nil
}
