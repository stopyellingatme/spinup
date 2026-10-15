package upgrade

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/migrate"
)

type pendingMigrations func(string, string, []string, migrate.Stage) ([]migrate.Migration, error)
type localDoctor func(context.Context, doctor.Scope, *doctor.Context) []doctor.Report

type phaseHooks struct {
	pending pendingMigrations
	doctor  localDoctor
	mu      sync.Mutex
	// Keep provenance across steps. Rebuild it from applied migrations on continue.
	changedBy map[string]map[string]string
}

func init() {
	SetPhases(newPhaseHooks(migrate.Pending, doctor.RunAll))
}

func newPhaseHooks(pending pendingMigrations, checks localDoctor) Phases {
	h := &phaseHooks{pending: pending, doctor: checks, changedBy: make(map[string]map[string]string)}
	return Phases{
		Before: func(ctx context.Context, s *Step) error { return h.migrations(ctx, s, migrate.Before) },
		Apply:  phaseApply, Deps: phaseDeps, GoFix: phaseGoFix,
		After:  func(ctx context.Context, s *Step) error { return h.migrations(ctx, s, migrate.After) },
		Verify: h.verify,
	}
}

func migrationContext(s *Step) *migrate.Ctx {
	from := s.Lock.TemplateVersion
	if s.From != nil {
		from = s.From.Version
	} else if s.Plan != nil {
		// The lock already contains To when a verification failure resumes.
		from = s.Plan.From
	}
	return &migrate.Ctx{Root: s.Worktree, From: from, To: s.To.Version, Answers: s.Answers, Lock: s.Lock, Runner: s.Runner}
}

func (h *phaseHooks) migrations(ctx context.Context, s *Step, stage migrate.Stage) error {
	c := migrationContext(s)
	ms, err := h.pending(c.From, c.To, s.Lock.AppliedMigrations, stage)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("run migration %s: %w\nTry: run spinup upgrade --continue", m.ID, err)
		}
		changes, err := migrate.PlanAll(c, []migrate.Migration{m})
		if err != nil {
			return err
		}
		ids, err := migrate.Run(c, []migrate.Migration{m})
		s.Answers = c.Answers
		s.Lock.AppliedMigrations = append(s.Lock.AppliedMigrations, ids...)
		if err != nil {
			return err
		}
		h.mu.Lock()
		if h.changedBy[s.Worktree] == nil {
			h.changedBy[s.Worktree] = make(map[string]string)
		}
		for _, change := range changes {
			h.changedBy[s.Worktree][change.Path] = m.ID
		}
		h.mu.Unlock()
		if err := phasePrint(s, "Migration %s: %s\n", m.ID, m.Desc); err != nil {
			return err
		}
	}
	return nil
}

func (h *phaseHooks) provenance(s *Step) map[string]string {
	owners := make(map[string]string)
	// Plan is pure. A migration may no longer report a change after Apply, so
	// retain the pre-Apply results when this process ran the migration.
	c := migrationContext(s)
	for _, stage := range []migrate.Stage{migrate.Before, migrate.After} {
		ms, err := h.pending(c.From, c.To, nil, stage)
		if err != nil {
			continue
		}
		for _, m := range ms {
			if !slices.Contains(s.Lock.AppliedMigrations, m.ID) {
				continue
			}
			changes, err := migrate.PlanAll(c, []migrate.Migration{m})
			if err != nil {
				continue // Attribution is optional. Verification must still run.
			}
			for _, change := range changes {
				owners[change.Path] = m.ID
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for path, id := range h.changedBy[s.Worktree] {
		owners[path] = id
	}
	return owners
}

func phasePrint(s *Step, format string, args ...any) error {
	if s.Out == nil {
		return nil
	}
	if _, err := fmt.Fprintf(s.Out, format, args...); err != nil {
		return fmt.Errorf("print upgrade progress: %w\nTry: use a writable output", err)
	}
	return nil
}
