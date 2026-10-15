package project

import "testing"

func TestBackupBackendValidation(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "pgbackrest", "pgdump", "walg", "unknown"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, err := Parse([]byte("[app]\nname = 'todo'\nmodule = 'example.com/todo'\n[backups]\nbackend = '" + name + "'\n"))
			if err != nil {
				t.Fatal(err)
			}
			err = c.Validate()
			if (err != nil) != (name == "unknown") {
				t.Fatalf("backend %q: %v", name, err)
			}
		})
	}
}
