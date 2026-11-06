package manifest

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkLockReadValidateWrite(b *testing.B) {
	lock := New()
	lock.CLIVersion, lock.TemplateVersion, lock.MinCLI = "1.0.0", "1.0.0", "1.0.0"
	lock.Packs = []string{"base", "api"}
	lock.Kit = KitRef{Module: "github.com/stopyellingatme/spinup/kit", Version: "1.0.0"}
	lock.Answers["app_name"] = "example"
	lock.Answers["module"] = "example.com/example"
	for i := range 20 {
		name := fmt.Sprintf("resource%02d", i)
		for _, file := range []string{"model.go", "routes.go", "queries.sql"} {
			path := "api/" + name + "/" + file
			lock.Files[path] = FileEntry{Class: Scaffold, Template: "api/files/" + file, Hash: Hash([]byte(path)), Mode: "0644"}
		}
	}
	path := filepath.Join(b.TempDir(), "spinup.lock")
	if err := lock.Save(path); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		loaded, err := Load(path)
		if err != nil {
			b.Fatal(err)
		}
		if err := loaded.Validate(); err != nil {
			b.Fatal(err)
		}
		if err := loaded.Save(path); err != nil {
			b.Fatal(err)
		}
	}
}
