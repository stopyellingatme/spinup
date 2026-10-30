package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// restoreServiceText names what a stop of both color projects stops. It only
// changes the printed text: the stop always acts on the whole color projects.
func restoreServiceText(s *Session, rel deploy.Release) string {
	if modules.Enabled(s.Config, "jobs") || len(rel.Services) > 0 {
		return "the app services"
	}
	return "api and web"
}

// releaseFiles returns the Compose files of the release in dir, as absolute paths.
func releaseFiles(dir string, rel deploy.Release) []string {
	files := deploy.ColorComposeFiles(rel.Services)
	for i := range files {
		files[i] = dir + "/" + files[i]
	}
	return files
}

// liveCompose runs a Compose command on the whole live color project, with no
// service names, so that it acts on every app service of the release.
func liveCompose(s *Session, rel deploy.Release, args ...string) []string {
	dir := "/opt/" + s.App + "/releases/" + rel.Release
	return deploy.ComposeArgv(s.App+"-"+rel.Color, dir, releaseFiles(dir, rel), args...)
}

func restoreRecovery(ctx context.Context, s *Session, rel deploy.Release, state *restoreRemote, starting bool, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	progress := s.Progress
	serviceText := restoreServiceText(s, rel)
	if strings.HasPrefix(serviceText, "the ") {
		serviceText = "The " + strings.TrimPrefix(serviceText, "the ")
	}
	postgres := baseCompose(s.App, "start", "--wait", "postgres")
	live := liveCompose(s, rel, "up", "-d", "--no-deps", "--wait")
	if !state.touched {
		var pgErr error
		if state.postgres != "running" {
			_, pgErr = s.Remote.Run(cleanup, remote.Cmd{Argv: postgres, Stdout: progress, Stderr: progress})
		}
		var appErr error
		if pgErr == nil {
			_, appErr = s.Remote.Run(cleanup, remote.Cmd{Argv: live, Stdout: progress, Stderr: progress})
		}
		cause = errors.Join(cause, pgErr, appErr)
		if pgErr == nil && appErr == nil {
			return fmt.Errorf("%w\nThe database was not changed. Restarted postgres and the live color.\nTry: inspect the server logs before retrying the restore", cause)
		}
		status := "postgres restarted. The live color failed to start. Its state is unconfirmed"
		if pgErr != nil {
			status = "postgres failed to restart. Its state is unconfirmed. " + serviceText + " were not restarted"
		}
		return fmt.Errorf("%w\nThe database was not changed. %s.\nTry: run these commands on the VM, in order:\n%s\n%s", cause, status, shellCommand(postgres), shellCommand(live))
	}
	// The live color can have started partially before an up or smoke failure.
	// Stop it again rather than leave traffic reaching an unverified database.
	var stopErr error
	if starting {
		_, stopErr = s.Remote.Run(cleanup, remote.Cmd{Argv: liveCompose(s, rel, "stop"), Stdout: progress, Stderr: progress})
	}
	status := serviceText + " are STOPPED"
	if stopErr != nil {
		status = serviceText + " stop failed. Their state is unconfirmed"
	}
	return fmt.Errorf("%w\n%s. postgres: %s. The database may have changed.\nTry: inspect the database before running these commands on the VM, in order:\n%s\n%s", errors.Join(cause, stopErr), status, state.postgres, shellCommand(postgres), shellCommand(live))
}
