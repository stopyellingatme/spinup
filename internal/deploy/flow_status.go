package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// StatusReport is the output of spinup deploy status.
type StatusReport struct {
	Env         string    `json:"env"`
	Host        string    `json:"host"`
	Live        string    `json:"live_color"`
	Current     *Release  `json:"current"`
	Lock        *LockInfo `json:"lock"`
	LockExpired bool      `json:"lock_expired"`
	// Journal holds the entries of the last deploy in deploy.state.
	Journal []Entry `json:"journal"`
	// Next is the step that deploy --resume would run, or "" when the last deploy is complete.
	Next string `json:"next_step"`
}

// target resolves the project and environment of a status or unlock command.
func target(o Options) (string, project.Env, error) {
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return "", project.Env{}, &ui.Error{What: "Cannot find the project", Why: "spinup.toml was not found", Try: "run the command inside a spinup project, or pass --dir"}
	}
	config, err := project.Load(root)
	if err != nil {
		return "", project.Env{}, &ui.Error{What: "Cannot load spinup.toml", Why: stripTry(err.Error()), Try: "correct spinup.toml"}
	}
	env, err := config.ResolveEnv(o.Env)
	if err != nil {
		return "", project.Env{}, &ui.Error{What: "Cannot resolve the environment", Why: stripTry(err.Error()), Try: "pass --env <name>"}
	}
	if _, err := appRoot(config.App.Name); err != nil {
		return "", project.Env{}, err
	}
	return config.App.Name, env, nil
}

func dial(ctx context.Context, o Options, d Deps, env project.Env) (remote.Runner, error) {
	vm, err := d.Dial(ctx, env, o.AcceptNewHostKey)
	if err != nil {
		return nil, &ui.Error{What: "Cannot connect to " + env.Name, Why: stripTry(err.Error()), Try: "check host, user, and port in spinup.toml and your SSH config", Err: err}
	}
	return vm, nil
}

// Status prints the current release, the lock, and the last journal of an environment. It only reads.
func Status(ctx context.Context, o Options, d Deps) error {
	o.fill()
	d.fill()
	app, env, err := target(o)
	if err != nil {
		return err
	}
	vm, err := dial(ctx, o, d, env)
	if err != nil {
		return err
	}
	defer func() { _ = vm.Close(context.WithoutCancel(ctx)) }()
	report := StatusReport{Env: env.Name, Host: env.Host, Journal: []Entry{}}
	upstream, err := ReadUpstream(ctx, vm, app)
	if err != nil {
		return err
	}
	report.Live = LiveColor(upstream)
	if report.Current, err = readRelease(ctx, vm, app, ""); err != nil {
		return err
	}
	if report.Lock, err = ReadLock(ctx, vm, app); err != nil {
		return err
	}
	if report.Lock != nil {
		expires := report.Lock.Acquired.Add(time.Duration(report.Lock.TTLSeconds) * time.Second)
		report.LockExpired = !d.Now().Before(expires)
	}
	entries, err := ReadJournal(ctx, vm, app)
	if err != nil {
		return err
	}
	if n := len(entries); n > 0 {
		last := entries[n-1].Release
		for _, e := range entries {
			if e.Release == last {
				report.Journal = append(report.Journal, e)
			}
		}
		report.Next, _ = NextStep(entries, Steps, last)
	}
	if o.JSON {
		enc := json.NewEncoder(o.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	writeStatus(o, report)
	return nil
}

func writeStatus(o Options, r StatusReport) {
	w := o.Out
	fmt.Fprintf(w, "Environment: %s (%s)\n", r.Env, r.Host)
	if r.Current == nil {
		fmt.Fprintln(w, "Current release: none")
	} else {
		c := r.Current
		fmt.Fprintf(w, "Current release: %s (%s color, commit %s, by %s at %s)\n", c.Release, c.Color, short(c.GitSHA), c.User, c.CreatedAt.UTC().Format(time.RFC3339))
	}
	if r.Live == "" {
		fmt.Fprintln(w, "Live color: none")
	} else {
		fmt.Fprintf(w, "Live color: %s\n", r.Live)
	}
	switch {
	case r.Lock == nil:
		fmt.Fprintln(w, "Lock: free")
	case r.LockExpired:
		fmt.Fprintf(w, "Lock: expired, held by %s (pid %d on %s) for release %s\n", r.Lock.Owner, r.Lock.PID, r.Lock.Host, r.Lock.Release)
	default:
		fmt.Fprintf(w, "Lock: held by %s (pid %d on %s) for release %s since %s\n", r.Lock.Owner, r.Lock.PID, r.Lock.Host, r.Lock.Release, r.Lock.Acquired.UTC().Format(time.RFC3339))
	}
	if len(r.Journal) == 0 {
		fmt.Fprintln(w, "Last deploy: none")
		return
	}
	fmt.Fprintf(w, "Last deploy: %s\n", r.Journal[0].Release)
	for _, e := range r.Journal {
		line := fmt.Sprintf("  %s %-9s %s", e.At.UTC().Format(time.RFC3339), e.Status, e.Step)
		if e.Status == "failed" && e.Detail != "" {
			line += ": " + firstLine(e.Detail)
		}
		fmt.Fprintln(w, line)
	}
	if r.Next != "" {
		fmt.Fprintf(w, "The last deploy is not complete. Next step: %s\nTry: spinup deploy --resume --env %s\n", r.Next, r.Env)
	}
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Unlock removes the deploy lock without an ownership check. It needs force.
func Unlock(ctx context.Context, o Options, d Deps, force bool) error {
	o.fill()
	d.fill()
	if !force {
		return &ui.Error{What: "Cannot unlock", Why: "unlock removes the lock of another deploy that may still run", Try: "check spinup deploy status, then run spinup deploy unlock --force"}
	}
	app, env, err := target(o)
	if err != nil {
		return err
	}
	vm, err := dial(ctx, o, d, env)
	if err != nil {
		return err
	}
	defer func() { _ = vm.Close(context.WithoutCancel(ctx)) }()
	info, err := ReadLock(ctx, vm, app)
	if err != nil {
		// Invalid metadata is a reason to unlock. Report it and continue.
		fmt.Fprintf(o.ErrOut, "Warning: %s\n", stripTry(err.Error()))
	}
	if err := ForceUnlock(ctx, vm, app); err != nil {
		return &ui.Error{What: "Cannot unlock", Why: stripTry(err.Error()), Try: "check the SSH connection", Err: err}
	}
	if info == nil {
		fmt.Fprintf(o.Out, "Removed the deploy lock of %s\n", env.Name)
	} else {
		fmt.Fprintf(o.Out, "Removed the deploy lock of %s (held by %s for release %s)\n", env.Name, info.Owner, info.Release)
	}
	fmt.Fprintln(o.Out, "The journal is unchanged. Run spinup deploy --resume to continue the last deploy.")
	return nil
}
