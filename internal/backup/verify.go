package backup

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Verify restores a backup, prints the result, and records verification metrics.
func Verify(ctx context.Context, o Options, t Target) (VerifyResult, error) {
	if err := validateTarget(t); err != nil {
		return VerifyResult{}, err
	}
	backend, s, err := Open(o)
	if err != nil {
		return VerifyResult{}, err
	}
	defer s.Close()
	result, verifyErr := backend.Verify(ctx, t)
	if !result.OK && verifyErr == nil {
		verifyErr = fmt.Errorf("verify backup %s: the restored database failed its checks\nTry: inspect the backup and run verification again", result.Label)
	}
	if verifyErr != nil {
		result.OK = false
	}
	metricsCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	metricErr := RecordMetrics(metricsCtx, s.Remote, s.App, Sample{Verified: time.Now().UTC(), VerifyOK: verifyErr == nil && result.OK, RTO: result.RTO})
	out, errOut := writers(o)
	if metricErr != nil {
		// A metrics failure never changes the verify result (ADR 0011 §4).
		_, _ = fmt.Fprintf(errOut, "warning: verify finished, but metrics could not be written: %v\n", metricErr)
	}
	var outputErr error
	if o.JSON {
		outputErr = writeJSON(out, result)
	} else {
		_, outputErr = fmt.Fprintf(out, "backup %s: RTO %s, OK %t\n", result.Label, result.RTO.Round(time.Millisecond), result.OK && verifyErr == nil)
	}
	return result, errors.Join(verifyErr, outputErr)
}

// Verify tests a physical restore and removes the container and its anonymous volume.
func (p *PGBackRest) Verify(ctx context.Context, t Target) (VerifyResult, error) {
	t, err := p.resolveTarget(ctx, t)
	if err != nil {
		return VerifyResult{}, err
	}
	return p.restoreContainer(ctx, t, true)
}
