package pgupgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/maintain/pg"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"go.yaml.in/yaml/v3"
)

func (workflow) Cleanup(ctx context.Context, s *pg.Session) error {
	data, err := s.Run(ctx, "cat", s.Details().OperationDir+"/link-state.json")
	if err != nil {
		return err
	}
	var st state
	if err = json.Unmarshal(data, &st); err != nil {
		return fail("read link upgrade state", err.Error())
	}
	if !st.Complete || !safePath(st.Old) || !safePath(st.New) || st.Old == st.New || strings.HasPrefix(st.Old, st.New+"/") || strings.HasPrefix(st.New, st.Old+"/") || st.From < 16 || st.To <= st.From || st.To > 18 || st.New != strconv.Itoa(st.To)+"/docker" || st.Restore == "" {
		return fail("cleanup old directory", "the upgrade state is incomplete or invalid")
	}
	if err = s.Inspect(ctx); err != nil {
		return err
	}
	d := s.Details()
	rel, err := oldPath(d)
	if err != nil {
		return err
	}
	if d.Volume != st.Volume || d.Current != st.To || rel != st.New {
		return fail("cleanup old directory", "the active cluster does not match the completed upgrade")
	}
	if err = checkRepository(d, st); err != nil {
		return err
	}
	image, err := pinned(st.To)
	if err != nil {
		return err
	}
	fmt.Fprintln(d.Out, "Permanently remove the old data directory. Never start the old cluster.")
	// Both paths are validated, disjoint subdirectories. Never remove the volume.
	script := pathGuard + `check_path "$1"
check_path "$2"
test -f "/pg/$2/PG_VERSION"
test ! -f "/pg/$1/global/pg_control"
rm -rf -- "/pg/$1"`
	if _, err = s.Run(ctx, helperArgs(d, image.Ref(), script, st.Old, st.New)...); err != nil {
		return err
	}
	// Keep the guarded rollback script and backup label as permanent recovery guidance.
	fmt.Fprintln(d.Out, "Retained recovery files and safety backup. Restore command:")
	fmt.Fprintln(d.Out, st.Restore)
	return nil
}

func checkRepository(d pg.Details, st state) error {
	lock, err := manifest.Load(filepath.Join(d.Root, "spinup.lock"))
	if err != nil {
		return fail("check repository before cleanup", err.Error())
	}
	if lock.Answers["db_version"] != strconv.Itoa(st.To) {
		return fail("check repository before cleanup", "spinup.lock answers.db_version must match the active major")
	}
	data, err := os.ReadFile(filepath.Join(d.Root, "deploy/compose.edge.yml"))
	if err != nil {
		return fail("check repository before cleanup", err.Error())
	}
	var doc struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
		Volumes map[string]struct {
			External bool   `yaml:"external"`
			Name     string `yaml:"name"`
		} `yaml:"volumes"`
	}
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return fail("check repository before cleanup", err.Error())
	}
	volume := doc.Volumes["postgres_data"]
	if !volume.External || volume.Name != st.Volume || doc.Services["postgres"].Environment["PGDATA"] != d.Data {
		return fail("check repository before cleanup", "commit the external volume name and the active PGDATA in deploy/compose.edge.yml")
	}
	return nil
}
