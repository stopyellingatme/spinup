package templates

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestKitMigrationNumbers(t *testing.T) {
	numbers := make(map[string]string)
	migration := regexp.MustCompile(`^([0-9]{5})_.+\.sql$`)
	err := filepath.WalkDir("packs", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		path = filepath.ToSlash(path)
		if strings.Contains(path, "/files/api/db/migrations/") && strings.HasSuffix(path, ".sql") && filepath.Base(path) != "00001_init.sql" {
			t.Errorf("pack migration in app space: %s", path)
		}
		if !strings.Contains(path, "/files/api/db/kit/") || !strings.HasSuffix(path, ".sql") {
			return nil
		}
		match := migration.FindStringSubmatch(filepath.Base(path))
		if match == nil {
			t.Errorf("invalid kit migration name: %s", path)
			return nil
		}
		if previous, ok := numbers[match[1]]; ok {
			t.Errorf("kit migration number %s is shared by %s and %s", match[1], previous, path)
		} else {
			numbers[match[1]] = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
