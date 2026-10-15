package backup

import (
	"context"
	"fmt"
	"strconv"
)

// PruneOptions selects retention execution or its read-only plan.
type PruneOptions struct {
	Options
	// Plan previews retention without removing backups.
	Plan bool
}

// Expire applies the configured pgBackRest retention policy.
func (p *PGBackRest) Expire(ctx context.Context) error {
	_, err := p.run(ctx, "expire pgBackRest backups", p.expireArgs(false)...)
	return err
}

// PlanExpire previews the pgBackRest retention policy without removing backups.
func (p *PGBackRest) PlanExpire(ctx context.Context) error {
	data, err := p.run(ctx, "plan pgBackRest expiration", p.expireArgs(true)...)
	if err == nil && p.Progress != nil {
		_, err = p.Progress.Write(data)
	}
	return err
}

// Prune expires backups and lists the backups that remain.
func Prune(ctx context.Context, o PruneOptions) ([]Info, error) {
	backend, s, err := Open(o.Options)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if o.Plan {
		if planner, ok := backend.(interface{ PlanExpire(context.Context) error }); ok {
			err = planner.PlanExpire(ctx)
		}
	} else {
		err = backend.Expire(ctx)
	}
	if err != nil {
		return nil, err
	}
	bs, err := backend.List(ctx)
	if err != nil {
		return nil, err
	}
	out, _ := writers(o.Options)
	if o.JSON {
		if bs == nil {
			bs = []Info{}
		}
		return bs, writeJSON(out, bs)
	}
	if o.Plan {
		fmt.Fprintln(out, "Prune plan: no backups removed")
	} else {
		fmt.Fprintln(out, "Backups remaining:")
	}
	for _, b := range bs {
		fmt.Fprintln(out, b.Label)
	}
	return bs, nil
}

func (p *PGBackRest) expireArgs(plan bool) []string {
	args := []string{"expire"}
	if plan {
		args = append(args, "--dry-run")
	}
	if p.RetentionFull > 0 {
		args = append(args, "--repo1-retention-full="+strconv.Itoa(p.RetentionFull))
	}
	return args
}
