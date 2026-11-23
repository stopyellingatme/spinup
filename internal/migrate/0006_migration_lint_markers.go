package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/stopyellingatme/spinup/internal/templates"
)

// MigrationLintMarkersID identifies the codemod for kit migration lint markers.
const MigrationLintMarkersID = "0006_migration_lint_markers"

var markerMigrations = []string{
	"auth/files/api/db/kit/00004_oidc_identities.sql",
	"audit/files/api/db/kit/00006_audit_events.sql",
	"flags/files/api/db/kit/00008_feature_flags.sql",
	"apikeys/files/api/db/kit/00009_api_keys.sql",
	"billing/files/api/db/kit/00010_billing.sql",
}

const migrationMarker = "-- spinup:allow-destructive adds audit triggers only when absent"

type markerEdit struct {
	change  Change
	content []byte
	manual  bool
}

func init() { Register(migrationLintMarkers()) }

func migrationLintMarkers() Migration {
	return Migration{
		ID: MigrationLintMarkersID, Since: "0.1.0-alpha.904", Desc: "Add lint markers to unchanged kit migrations", Stage: After,
		Plan: func(c *Ctx) ([]Change, error) {
			edits, err := migrationMarkerEdits(c)
			changes := make([]Change, 0, len(edits))
			for _, edit := range edits {
				changes = append(changes, edit.change)
			}
			return changes, err
		},
		Apply: func(c *Ctx) error {
			edits, err := migrationMarkerEdits(c)
			if err != nil {
				return err
			}
			root, err := os.OpenRoot(c.Root)
			if err != nil {
				return markerError(".", err)
			}
			defer root.Close()
			for _, edit := range edits {
				if edit.manual {
					fmt.Printf("%s: add by hand: %s\n", edit.change.Path, migrationMarker)
					continue
				}
				info, err := root.Stat(edit.change.Path)
				if err != nil {
					return markerError(edit.change.Path, err)
				}
				if err := root.WriteFile(edit.change.Path, edit.content, info.Mode().Perm()); err != nil {
					return markerError(edit.change.Path, err)
				}
			}
			return nil
		},
	}
}

func migrationMarkerEdits(c *Ctx) ([]markerEdit, error) {
	root, err := os.OpenRoot(c.Root)
	if err != nil {
		return nil, markerError(".", err)
	}
	defer root.Close()
	var edits []markerEdit
	for _, packPath := range markerMigrations {
		name := packPath[strings.LastIndex(packPath, "/")+1:]
		path := "api/db/kit/" + name
		info, err := root.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, markerError(path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, markerError(path, errors.New("not a regular file"))
		}
		current, err := root.ReadFile(path)
		if err != nil {
			return nil, markerError(path, err)
		}
		target, err := fs.ReadFile(templates.Embedded(), packPath)
		if err != nil {
			return nil, markerError(path, err)
		}
		if bytes.Equal(current, target) {
			continue
		}
		var old []byte
		for _, line := range bytes.SplitAfter(target, []byte("\n")) {
			if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("-- spinup:allow-destructive")) {
				old = append(old, line...)
			}
		}
		if bytes.Equal(current, old) {
			edits = append(edits, markerEdit{change: Change{Path: path, Desc: "Add migration lint markers"}, content: target})
		} else {
			edits = append(edits, markerEdit{change: Change{Path: path, Desc: "Manual step: add " + migrationMarker + " before each unmarked dynamic SQL block"}, manual: true})
		}
	}
	return edits, nil
}

func markerError(path string, err error) error {
	return fmt.Errorf("update migration lint markers in %s: %w\nTry: check that the migration is a readable regular file and run spinup upgrade --continue", path, err)
}
