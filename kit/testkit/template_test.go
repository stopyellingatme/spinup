package testkit

import (
	"testing"
	"testing/fstest"
)

func TestMigrationsHashIncludesKitSpace(t *testing.T) {
	t.Parallel()
	fsys := func(sql string) fstest.MapFS { return fstest.MapFS{"00001_step.sql": {Data: []byte(sql)}} }
	app, kit := fsys("app"), fsys("kit")
	hash := func(app, kit fstest.MapFS) string {
		t.Helper()
		got, err := migrationsHash(app, kit)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	both := hash(app, kit)
	for _, other := range []string{hash(app, nil), hash(kit, app), hash(app, fsys("changed")), hash(fsys("changed"), kit)} {
		if both == other {
			t.Fatal("hash did not distinguish migration spaces or contents")
		}
	}
	if both != hash(app, kit) {
		t.Fatal("hash is not deterministic")
	}
}
