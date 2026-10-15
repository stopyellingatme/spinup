package migrate

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/stopyellingatme/spinup/internal/compat"
)

// Registry stores migrations. Its zero value is ready for use.
type Registry struct {
	mu         sync.RWMutex
	migrations []Migration
}

var registry Registry

// Register registers m during init. It panics if m is invalid or duplicated.
func Register(m Migration) { registry.Register(m) }

// All returns registered migrations sorted by semantic version, then ID.
func All() []Migration { return registry.All() }

// Pending selects unapplied migrations in the interval from < Since <= to.
func Pending(from, to string, applied []string, stage Stage) ([]Migration, error) {
	return registry.Pending(from, to, applied, stage)
}

// Register adds m. It panics if m is invalid or its ID already exists.
func (r *Registry) Register(m Migration) {
	if strings.TrimSpace(m.ID) == "" {
		panic("migrate: empty migration ID")
	}
	if _, err := compat.ParseVersion(m.Since); err != nil {
		panic(fmt.Sprintf("migrate: migration %q: %v", m.ID, err))
	}
	if m.Stage == 0 || m.Plan == nil || m.Apply == nil {
		panic(fmt.Sprintf("migrate: migration %q requires a stage, Plan, and Apply", m.ID))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.migrations {
		if existing.ID == m.ID {
			panic(fmt.Sprintf("migrate: duplicate migration %q", m.ID))
		}
	}
	r.migrations = append(r.migrations, m)
}

// All returns an independent slice in semantic version and ID order.
func (r *Registry) All() []Migration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ms := append([]Migration(nil), r.migrations...)
	sort.Slice(ms, func(i, j int) bool {
		a, _ := compat.ParseVersion(ms[i].Since)
		b, _ := compat.ParseVersion(ms[j].Since)
		if cmp := a.Compare(b); cmp != 0 {
			return cmp < 0
		}
		return ms[i].ID < ms[j].ID
	})
	return ms
}

// Pending returns migrations for stage whose versions fall in (from, to].
func (r *Registry) Pending(from, to string, applied []string, stage Stage) ([]Migration, error) {
	low, err := compat.ParseVersion(from)
	if err != nil {
		return nil, fmt.Errorf("select migrations: invalid source version: %w\nTry: check the source template version", err)
	}
	high, err := compat.ParseVersion(to)
	if err != nil {
		return nil, fmt.Errorf("select migrations: invalid target version: %w\nTry: choose a valid target template version", err)
	}
	if low.Compare(high) > 0 {
		return nil, fmt.Errorf("select migrations: source %q exceeds target %q\nTry: choose a target version at or after the source", from, to)
	}
	done := make(map[string]bool, len(applied))
	for _, id := range applied {
		done[id] = true
	}
	var pending []Migration
	for _, m := range r.All() {
		since, _ := compat.ParseVersion(m.Since)
		if m.Stage == stage && !done[m.ID] && low.Compare(since) < 0 && since.Compare(high) <= 0 {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

// PlanAll plans ms in the supplied order and returns changes before any error.
func PlanAll(c *Ctx, ms []Migration) ([]Change, error) {
	var changes []Change
	for _, m := range ms {
		planned, err := m.Plan(c)
		if err != nil {
			return changes, fmt.Errorf("plan migration %q: %w\nTry: correct the migration input and run spinup upgrade --plan", m.ID, err)
		}
		changes = append(changes, planned...)
	}
	return changes, nil
}

// Run applies ms in the supplied order and returns IDs completed before an error.
// The caller records these IDs in the lock.
func Run(c *Ctx, ms []Migration) (applied []string, err error) {
	for _, m := range ms {
		if err := m.Apply(c); err != nil {
			return applied, fmt.Errorf("apply migration %q: %w\nTry: correct the migration input and run spinup upgrade --continue", m.ID, err)
		}
		applied = append(applied, m.ID)
	}
	return applied, nil
}
