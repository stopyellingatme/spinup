package backup

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/stopyellingatme/spinup/internal/remote"
)

// ShowScheduleStatus prints installed timers and the metrics file from the VM.
// It omits the timers when systemd or the installed units are absent.
func ShowScheduleStatus(ctx context.Context, r remote.Runner, app string, out io.Writer) error {
	timers := scheduleTimers(app)
	names := make([]string, 0, len(timers))
	checks := make([]string, 0, len(timers))
	for _, name := range timers {
		names = append(names, remote.Quote(name))
		checks = append(checks, "[ -f "+remote.Quote("/etc/systemd/system/"+name)+" ]")
	}
	script := "if " + systemdProbe + " && { " + strings.Join(checks, " || ") + "; }; then\n systemctl list-timers --all --no-pager " + strings.Join(names, " ") + "\nfi\n"
	filename := remote.Quote(MetricsPath(app))
	script += "if [ -f " + filename + " ]; then\n printf '%s\\n' 'backup metrics:'\n cat " + filename + "\nfi"
	if _, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-ec", script}, Stdout: out}); err != nil {
		return fmt.Errorf("read backup timers and metrics: %w\nTry: check systemd and permissions of %s on the VM", err, MetricsPath(app))
	}
	return nil
}
