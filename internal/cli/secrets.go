package cli

import (
	"os"

	"github.com/spf13/cobra"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/secrets"
)

func init() { Register(GroupOperate, newSecrets) }

func secretsOptions(app *App, c *cobra.Command) secrets.Options {
	return secrets.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, Yes: app.Flags.Yes, CI: os.Getenv("CI") != "", ConfirmName: app.Flags.ConfirmEnv, Interactive: app.UI.Interactive(), DryRun: app.Flags.DryRun, In: c.InOrStdin(), Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}
}

// secretsSubcommands allows deployment units to register additional operations.
var secretsSubcommands []func(*App) *cobra.Command

// RegisterSecrets adds a secrets operation during package initialization.
func RegisterSecrets(build func(*App) *cobra.Command) {
	secretsSubcommands = append(secretsSubcommands, build)
}

func newSecrets(app *App) *cobra.Command {
	group := &cobra.Command{Use: "secrets", Short: "Manage encrypted secrets for each environment", Args: cobra.NoArgs}
	var recovery, ci string
	var noRecovery bool
	initCmd := &cobra.Command{Use: "init", Short: "Create an environment key and encrypted secrets", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := secretsOptions(app, c)
		o.RecoveryRecipient, o.CIRecipient, o.NoRecovery = recovery, ci, noRecovery
		return secrets.Init(c.Context(), o, runexec.OSRunner{})
	}}
	initCmd.Flags().StringVar(&recovery, "recovery-recipient", "", "age recipient for recovery (required unless --no-recovery)")
	initCmd.Flags().StringVar(&ci, "ci-recipient", "", "age recipient for CI")
	initCmd.Flags().BoolVar(&noRecovery, "no-recovery", false, "accept permanent loss if all environment keys are lost")
	group.AddCommand(initCmd)
	for _, operation := range []struct {
		name, short string
		run         func(*cobra.Command) error
	}{
		{"edit", "Edit secrets in a terminal", func(c *cobra.Command) error {
			return secrets.Edit(c.Context(), secretsOptions(app, c), runexec.OSRunner{})
		}},
		{"diff", "Show names changed since Git HEAD", func(c *cobra.Command) error {
			return secrets.Diff(c.Context(), secretsOptions(app, c), runexec.OSRunner{})
		}},
		{"rotate", "Replace the data encryption key", func(c *cobra.Command) error {
			return secrets.Rotate(c.Context(), secretsOptions(app, c), runexec.OSRunner{})
		}},
		{"rekey", "Update encryption recipients from .sops.yaml", func(c *cobra.Command) error {
			return secrets.Rekey(c.Context(), secretsOptions(app, c), runexec.OSRunner{})
		}},
	} {
		group.AddCommand(&cobra.Command{Use: operation.name, Short: operation.short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return operation.run(c) }})
	}
	group.AddCommand(&cobra.Command{Use: "get <NAME>", Short: "Write one secret value to stdout", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return secrets.Get(c.Context(), secretsOptions(app, c), args[0], runexec.OSRunner{})
	}})
	group.AddCommand(&cobra.Command{Use: "set <NAME>", Short: "Read a secret value from stdin", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return secrets.Set(c.Context(), secretsOptions(app, c), args[0], runexec.OSRunner{})
	}})
	for _, build := range secretsSubcommands {
		group.AddCommand(build(app))
	}
	return group
}
