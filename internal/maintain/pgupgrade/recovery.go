package pgupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/maintain/pg"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/modules"
)

const removeUnlinkedScript = pathGuard + `old=$1; new=$2
check_path "$old"
check_path "$new"
check_path "$new.spinup-upgrade"
test -f "/pg/$old/PG_VERSION"
test -f "/pg/$old/global/pg_control"
if [ -f "/pg/$new.spinup-upgrade" ]; then
 rm -rf -- "/pg/$new"
 rm -f -- "/pg/$new.spinup-upgrade"
elif [ -e "/pg/$new" ]; then
 echo 'Target cleanup refused: the directory has no upgrade ownership marker.' >&2
 echo 'Try: inspect the target directory before retrying.' >&2
 exit 1
fi`

func recoverBeforeLink(ctx context.Context, s *pg.Session, helper string, st state, stopping, initializing bool) error {
	d := s.Details()
	var cleanupErr error
	if initializing {
		_, cleanupErr = s.Run(ctx, helperArgs(d, helper, removeUnlinkedScript, st.Old, st.New)...)
	}
	if stopping {
		// Restart the intact source even when target cleanup needs manual inspection.
		if err := restartOriginal(ctx, s); err != nil {
			return errors.Join(cleanupErr, err)
		}
		fmt.Fprintln(d.Out, "Restarted the original PostgreSQL cluster and the live color. The link upgrade did not start.")
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	if err := s.ReleaseRecovery(ctx); err != nil {
		return err
	}
	fmt.Fprintln(d.Out, "Released the recovery directory. Correct the failure before retrying the upgrade.")
	return nil
}

func restartOriginal(ctx context.Context, s *pg.Session) error {
	if _, err := s.Run(ctx, s.Base("start", "--wait", "postgres")...); err != nil {
		return err
	}
	d := s.Details()
	data, err := s.Run(ctx, "cat", d.Dir+"/release.json")
	if err != nil {
		return err
	}
	var rel deploy.Release
	if err = json.Unmarshal(data, &rel); err != nil {
		return fail("read live color for recovery", err.Error())
	}
	if rel.Color != deploy.Blue && rel.Color != deploy.Green {
		return fail("read live color for recovery", "the release color is invalid")
	}
	services := []string{"api"}
	if lock, err := manifest.Load(filepath.Join(d.Root, "spinup.lock")); err == nil && modules.WebEnabled(lock) {
		services = append(services, "web")
	}
	args := deploy.ComposeArgv(d.Config.App.Name+"-"+rel.Color, d.Dir, []string{d.Dir + "/compose.yml"}, append([]string{"start", "--wait"}, services...)...)
	_, err = s.Run(ctx, args...)
	return err
}
