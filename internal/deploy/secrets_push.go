package deploy

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/secrets"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// PushSecrets decrypts the environment's secrets locally and writes them to the host
// (ADR 0009 §5) under the deploy lock, without a deploy. Running containers read the
// files at start, so the new values take effect at the next deploy or restart.
func PushSecrets(ctx context.Context, o Options, d Deps) error {
	o.fill()
	d.fill()
	app, env, err := target(o)
	if err != nil {
		return err
	}
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return err
	}
	if err := project.ConfirmProtected(env, "secrets push", o.Confirm); err != nil {
		return err
	}
	values, err := d.Decrypt(ctx, root, env.Name)
	if err != nil {
		return &ui.Error{What: "Cannot push secrets", Why: stripTry(err.Error()), Try: "check the environment key (spinup secrets init) and secrets/" + env.Name + ".enc.yaml", Err: err}
	}
	vm, err := dial(ctx, o, d, env)
	if err != nil {
		return err
	}
	defer func() { _ = vm.Close(context.WithoutCancel(ctx)) }()
	lock, err := AcquireLock(ctx, vm, app, LockOptions{Owner: d.User(), Release: "secrets-push", TTL: o.LockTTL, Now: d.Now, Warn: o.ErrOut})
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release(context.WithoutCancel(ctx)) }()
	redact := make([]string, 0, len(values))
	names := make([]string, 0, len(values))
	for name, value := range values {
		redact = append(redact, value)
		names = append(names, name)
	}
	slices.Sort(names)
	if err := secrets.Push(ctx, uploader{vm: vm, redact: redact}, app, values); err != nil {
		return &ui.Error{What: "Cannot push secrets", Why: stripTry(err.Error()), Try: "check the SSH connection and /opt/" + app + "/secrets on the host", Err: err}
	}
	fmt.Fprintf(o.Out, "Pushed %d secrets to %s: %s\n", len(names), env.Name, strings.Join(names, ", "))
	fmt.Fprintf(o.Out, "Next: spinup deploy --env %s (running containers read the files at start)\n", env.Name)
	return nil
}
