package deploy

import "context"

// HookOptions are the deploy flags and facts that a hook can read from its context.
// HookContext (ADR 0009 §3) has no field for them, so the deployer passes them in the context.
type HookOptions struct {
	// SkipBackup is true for spinup deploy --skip-backup.
	SkipBackup bool
	// Root is the local project root.
	Root string
	// PendingMigrations are the local migration files (api/db/migrations) newer than the
	// migration version of the current release. All files are pending on the first deploy.
	PendingMigrations []string
}

type hookOptionsKey struct{}

// WithHookOptions returns a context that carries o to the hooks.
func WithHookOptions(ctx context.Context, o HookOptions) context.Context {
	return context.WithValue(ctx, hookOptionsKey{}, o)
}

// HookOptionsFrom returns the options of the deploy that runs the hook.
// The zero value means that the caller is not spinup deploy.
func HookOptionsFrom(ctx context.Context) HookOptions {
	o, _ := ctx.Value(hookOptionsKey{}).(HookOptions)
	return o
}
