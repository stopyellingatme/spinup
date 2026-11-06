package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/manifest"
)

// KitMigrationsID identifies the codemod that separates kit and app migrations.
const KitMigrationsID = "0003_kit_migrations"

var kitMigrationNames = []string{
	"00002_create_auth.sql",
	"00003_password_resets.sql",
	"00004_oidc_identities.sql",
	"00005_users_role.sql",
	"00006_audit_events.sql",
	"00007_users_auth_version.sql",
}

func init() { Register(kitMigrations()) }

func kitMigrations() Migration {
	return Migration{
		ID:    KitMigrationsID,
		Since: "0.1.0-alpha.419",
		Desc:  "Move pack migrations to api/db/kit",
		Stage: Before,
		Plan: func(c *Ctx) ([]Change, error) {
			root, err := os.OpenRoot(c.Root)
			if err != nil {
				return nil, kitMigrationError(err)
			}
			defer root.Close()
			return planKitMigrations(root, c.Lock)
		},
		Apply: func(c *Ctx) error {
			root, err := os.OpenRoot(c.Root)
			if err != nil {
				return kitMigrationError(err)
			}
			defer root.Close()
			changes, err := planKitMigrations(root, c.Lock)
			if err != nil || len(changes) == 0 {
				return err
			}
			if err := root.MkdirAll("api/db/kit", 0o755); err != nil {
				return kitMigrationError(err)
			}
			for _, change := range changes {
				name := strings.TrimPrefix(change.Path, "api/db/migrations/")
				target := "api/db/kit/" + name
				source, err := readKitMigration(root, change.Path)
				if err != nil {
					return err
				}
				destination, err := readKitMigration(root, target)
				switch {
				case errors.Is(err, fs.ErrNotExist):
					if err := root.Rename(change.Path, target); err != nil {
						return kitMigrationError(err)
					}
				case err != nil:
					return err
				case !bytes.Equal(source, destination):
					return fmt.Errorf("move kit migration %s: %s has different bytes\nTry: remove one of the two copies", change.Path, target)
				default:
					if err := root.Remove(change.Path); err != nil {
						return kitMigrationError(err)
					}
				}
			}
			return nil
		},
	}
}

func planKitMigrations(root *os.Root, lock *manifest.Lock) ([]Change, error) {
	entries, err := fs.ReadDir(root.FS(), "api/db/migrations")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, kitMigrationError(err)
	}
	var changes []Change
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 6 || name[5] != '_' || strings.IndexFunc(name[:5], func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			continue
		}
		for _, pack := range kitMigrationNames {
			owner := "auth"
			if pack == "00006_audit_events.sql" {
				owner = "audit"
			}
			if lock == nil || !slices.Contains(lock.Packs, owner) {
				continue
			}

			if name[5:] != pack[5:] {
				continue
			}
			path := "api/db/migrations/" + name
			if name != pack {
				changes = append(changes, Change{Path: path, Desc: "Manual step: " + name + " is a renamed kit migration"})
				return changes, fmt.Errorf("move kit migration %s: the pack number is %s\nTry: move the file to api/db/kit under its own number, run UPDATE goose_db_version SET version_id = %s WHERE version_id = %s once in every database, then rename the file to the pack name and run spinup upgrade again", name, pack[:5], pack[:5], name[:5])
			}
			if _, err := readKitMigration(root, path); err != nil {
				return changes, err
			}
			changes = append(changes, Change{Path: path, Desc: "Move " + name + " to api/db/kit"})
		}
	}
	return changes, nil
}

func readKitMigration(root *os.Root, path string) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, kitMigrationError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("read kit migration %s: not a regular file\nTry: replace the path with a regular SQL file", path)
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return nil, kitMigrationError(err)
	}
	return data, nil
}

func kitMigrationError(err error) error {
	return fmt.Errorf("move kit migrations: %w\nTry: check permissions on api/db/migrations and api/db/kit", err)
}
