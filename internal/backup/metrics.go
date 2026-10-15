package backup

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func init() { recordMetrics = writeMetrics }

func writeMetrics(ctx context.Context, r remote.Runner, app string, s Sample) error {
	if r == nil {
		return fmt.Errorf("write backup metrics: the remote runner is missing\nTry: check the VM connection")
	}
	if !s.Success.IsZero() || s.Size != 0 {
		if _, err := ParseKind(string(s.Kind)); err != nil || s.Kind == "" {
			return fmt.Errorf("write backup metrics: invalid backup kind %q\nTry: use full, diff, or incr", s.Kind)
		}
	}
	updates := metricUpdates(s)
	if len(updates) == 0 {
		return nil
	}
	filename := MetricsPath(app)
	if err := ensureMetricsDir(ctx, r, app); err != nil {
		return err
	}
	// Keep the read, merge, and rename under the same lock as run.sh.
	_, err := r.Run(ctx, remote.Cmd{
		Argv:  []string{"flock", path.Dir(filename) + "/.lock", "sh", "-ec", metricsMergeScript, "spinup-metrics", filename},
		Stdin: strings.NewReader(metricLines(updates)),
	})
	if err != nil {
		return fmt.Errorf("write backup metrics: %w\nTry: check flock, permissions, and disk space at %s", err, filename)
	}
	return nil
}

const metricsMergeScript = `metrics=$1
umask 077
tmp=$(mktemp "$metrics.tmp.XXXXXX")
trap 'rm -f "$tmp" "$tmp.old" "$tmp.updates" "$tmp.new"' 0
trap 'exit 1' HUP INT TERM
cat > "$tmp.updates"
if [ -e "$metrics" ]; then cat "$metrics" > "$tmp.old"; else : > "$tmp.old"; fi
awk 'NR == FNR {replace[$1]=1; next} /^#/ || !($1 in replace)' "$tmp.updates" "$tmp.old" > "$tmp.new"
cat "$tmp.updates" >> "$tmp.new"
LC_ALL=C sort "$tmp.new" > "$tmp"
chmod 0644 "$tmp"
mv -f "$tmp" "$metrics"`

func metricUpdates(s Sample) map[string]string {
	updates := map[string]string{}
	if !s.Success.IsZero() || s.Size != 0 {
		label := `{kind=` + strconv.Quote(string(s.Kind)) + `}`
		if !s.Success.IsZero() {
			updates["backup_last_success_timestamp_seconds"+label] = strconv.FormatInt(s.Success.Unix(), 10)
		}
		if s.Size != 0 {
			updates["backup_last_size_bytes"+label] = strconv.FormatInt(s.Size, 10)
		}
	}
	if !s.Verified.IsZero() {
		updates["backup_last_verify_timestamp_seconds"] = strconv.FormatInt(s.Verified.Unix(), 10)
		updates["backup_last_verify_ok"] = "0"
		if s.VerifyOK {
			updates["backup_last_verify_ok"] = "1"
		}
		updates["backup_last_verify_rto_seconds"] = strconv.FormatFloat(s.RTO.Seconds(), 'f', -1, 64)
	}
	return updates
}

func metricLines(updates map[string]string) string {
	lines := make([]string, 0, len(updates))
	for name, value := range updates {
		lines = append(lines, name+" "+value)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func backupMetrics(ctx context.Context, r remote.Runner, app string, result Result, warnings io.Writer) {
	stop := result.Stop
	if stop.IsZero() {
		stop = time.Now().UTC()
	}
	if err := RecordMetrics(ctx, r, app, Sample{Kind: result.Kind, Success: stop, Size: result.Size}); err != nil {
		_, _ = fmt.Fprintf(warnings, "warning: backup succeeded, but metrics could not be written: %v\n", err)
	}
}

func ensureMetricsDir(ctx context.Context, r remote.Runner, app string) error {
	dir := path.Dir(MetricsPath(app))
	if _, err := r.Run(ctx, remote.Cmd{Argv: []string{"mkdir", "-p", dir}}); err != nil {
		return fmt.Errorf("create metrics directory: %w\nTry: check permissions of %s", err, dir)
	}
	chmod := remote.Cmd{Argv: []string{"chmod", "0755", dir}}
	if _, err := r.Run(ctx, chmod); err != nil {
		// Docker can create a missing bind source as root before the first backup.
		// Match the parent owner so subsequent uploads work as the deploy user.
		if _, err = r.Run(ctx, remote.Cmd{Argv: []string{"sudo", "-n", "chown", "--reference=" + path.Dir(dir), dir}}); err != nil {
			return fmt.Errorf("set metrics directory owner: %w\nTry: give the deploy user ownership of %s", err, dir)
		}
		if _, err = r.Run(ctx, chmod); err != nil {
			return fmt.Errorf("set metrics directory permissions: %w\nTry: check ownership of %s", err, dir)
		}
	}
	return nil
}
