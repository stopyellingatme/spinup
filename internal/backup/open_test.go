package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestOpenSelection(t *testing.T) {
	sentinel := &PGBackRest{App: "selected-pgdump"}
	backendsMu.Lock()
	previous, existed := backends["pgdump"]
	backends["pgdump"] = func(Env) (Backend, error) { return sentinel, nil }
	backendsMu.Unlock()
	t.Cleanup(func() {
		backendsMu.Lock()
		defer backendsMu.Unlock()
		if existed {
			backends["pgdump"] = previous
		} else {
			delete(backends, "pgdump")
		}
	})
	for _, tt := range []struct {
		name string
		want string
	}{
		{"", "todo"}, {"pgdump", "selected-pgdump"}, {"unknown", "unknown backup backend"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := newProject(t, tt.name != "pgdump", "", nil)
			f, err := os.OpenFile(filepath.Join(dir, "spinup.toml"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString("\n[backups]\nbackend = \"" + tt.name + "\"\n")
			if closeErr := f.Close(); err != nil || closeErr != nil {
				t.Fatalf("write config: %v %v", err, closeErr)
			}
			vm := &remote.Fake{}
			b, s, err := Open(Options{Dir: dir, Env: "staging", Remote: vm})
			if tt.name == "unknown" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if b.(*PGBackRest).App != tt.want {
				t.Fatalf("backend: %+v", b)
			}
			vm.AssertDone(t)
		})
	}
}
