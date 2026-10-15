package rpc

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

var checkRegistrations struct {
	sync.Mutex
	ids map[string]bool
}

// ProjectChecks resolves trusted exec plugins and returns their doctor checks.
// The CLI can call this function only after it finds a project root.
func ProjectChecks(ctx context.Context, o Options) ([]doctor.Check, error) {
	plugins, err := plugin.Resolve(ctx, plugin.Options{Root: o.Root, Offline: o.Offline})
	if err != nil {
		return nil, err
	}
	var checks []doctor.Check
	seen := map[string]bool{}
	for _, p := range plugins {
		cfg, err := pack.ReadExec(p.FS)
		if err != nil {
			return nil, err
		}
		if cfg.Kind != "exec" {
			continue
		}
		client := &Client{Plugin: p, Root: o.Root, Runner: o.Runner, Stderr: o.Stderr}
		description, err := client.Describe(ctx)
		if err != nil {
			return nil, err
		}
		for _, spec := range description.Checks {
			id := "plugin/" + p.Meta.Name + "/" + spec.Name
			if !identifier.MatchString(spec.Name) || seen[id] {
				return nil, fmt.Errorf("load exec check %q: invalid or duplicate name\nTry: rename the plugin check", id)
			}
			seen[id] = true
			checks = append(checks, doctor.Check{ID: id, Title: spec.Title, Scope: doctor.Local, Run: func(ctx context.Context, c *doctor.Context) doctor.Result {
				if c == nil || c.Root != o.Root {
					return doctor.Result{Status: doctor.Skip, Message: "Plugin check belongs to another project"}
				}
				result, err := client.Check(ctx, sdk.CheckParams{Check: spec.Name})
				if err != nil {
					return doctor.Result{Status: doctor.Fail, Message: err.Error(), Try: "verify the exec plugin"}
				}
				return checkResult(result)
			}})
		}
	}
	return checks, nil
}

// RegisterProjectChecks registers discovered checks through the doctor registry.
// Registration is idempotent per check ID. A CLI process handles one project.
func RegisterProjectChecks(ctx context.Context, o Options) error {
	checks, err := ProjectChecks(ctx, o)
	if err != nil {
		return err
	}
	checkRegistrations.Lock()
	defer checkRegistrations.Unlock()
	if checkRegistrations.ids == nil {
		checkRegistrations.ids = map[string]bool{}
	}
	for _, check := range checks {
		if !checkRegistrations.ids[check.ID] {
			doctor.Register(check)
			checkRegistrations.ids[check.ID] = true
		}
	}
	return nil
}

func checkResult(result sdk.CheckResult) doctor.Result {
	report := doctor.Result{Status: doctor.OK, Message: "Plugin reported no findings"}
	var messages, tries []string
	priority := -1
	for _, finding := range result.Findings {
		status := doctor.OK
		rank := 1
		switch finding.Status {
		case "ok":
		case "skip":
			status = doctor.Skip
			rank = 0
		case "warn":
			status = doctor.Warn
			rank = 2
		case "fail":
			status = doctor.Fail
			rank = 3
		default:
			return doctor.Result{Status: doctor.Fail, Message: "Plugin returned an invalid finding status", Try: "correct the exec plugin"}
		}
		if rank > priority {
			priority = rank
			report.Status = status
		}
		messages = append(messages, finding.Message)
		if finding.Try != "" {
			tries = append(tries, finding.Try)
		}
	}
	if len(messages) > 0 {
		report.Message = strings.Join(messages, "\n")
	}
	report.Try = strings.Join(tries, "\n")
	return report
}
