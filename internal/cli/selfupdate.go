package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/selfupdate"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() { Register(GroupMaintain, newSelfUpdate) }

func newSelfUpdate(app *App) *cobra.Command {
	var options selfupdate.Options
	c := &cobra.Command{Use: "self-update", Short: "Verify and install a spinup release", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if err := updateOnline(app); err != nil {
			return err
		}
		options.Current = currentVersion().Version
		options.Check = options.Check || app.Flags.DryRun
		result, err := (selfupdate.Updater{}).Update(c.Context(), options)
		if err != nil {
			return err
		}
		return printUpdateResult(app, result)
	}}
	c.Flags().BoolVar(&options.Check, "check", false, "check for a release without installing it")
	c.Flags().StringVar(&options.Channel, "channel", "stable", "release channel: stable or next")
	c.Flags().StringVar(&options.Version, "version", "", "install this release version")
	c.Flags().BoolVar(&options.RequireSignature, "require-signature", false, "require cosign signature verification")
	return c
}

func updateOnline(app *App) error {
	if app.Flags.Offline {
		return &ui.Error{What: "Cannot check spinup releases", Why: "offline mode disables downloads", Try: "remove --offline and unset SPINUP_OFFLINE", Code: ui.ExitUsage}
	}
	return nil
}

func printUpdateResult(app *App, r selfupdate.Result) error {
	if r.Warning != "" {
		app.UI.Warnf("%s", r.Warning)
	}
	if app.Flags.JSON {
		return app.UI.JSON(r)
	}
	switch {
	case r.Command != "":
		app.UI.Println(r.Command)
	case r.Updated:
		app.UI.Printf("Updated spinup to %s\n", r.Version)
	case r.Available:
		app.UI.Printf("spinup %s is available (current %s)\n", r.Version, r.Current)
	default:
		app.UI.Printf("spinup %s is up to date\n", r.Current)
	}
	return nil
}

func printUpdateNotice(c *cobra.Command, app *App, errOut io.Writer) {
	// Explicit checks already query the release service.
	if c.Name() == "self-update" {
		return
	}
	if c.Name() == "version" {
		if check, _ := c.Flags().GetBool("check"); check {
			return
		}
	}
	_, ci := os.LookupEnv("CI")
	notice := (selfupdate.Updater{}).Notice(c.Context(), selfupdate.NoticeOptions{
		Current: currentVersion().Version, TTY: app.UI.Interactive(), JSON: app.Flags.JSON,
		Offline: app.Flags.Offline, CI: ci, Disabled: os.Getenv("SPINUP_NO_UPDATE_CHECK") == "1",
	})
	if notice != "" {
		_, _ = fmt.Fprintln(errOut, notice)
	}
}
