package pg

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestServicesUsesWebPack(t *testing.T) {
	t.Parallel()
	for _, web := range []bool{false, true} {
		t.Run(map[bool]string{false: "API only", true: "web pack"}[web], func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := manifest.New()
			lock.Packs = []string{"base", "api"}
			if web {
				lock.Packs = append(lock.Packs, "web")
			}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			u := &upgrade{root: root, cfg: &project.Config{Modules: []string{"ci", "deploy"}}}
			want := []string{"api"}
			if web {
				want = append(want, "web")
			}
			if got := u.services(); !slices.Equal(got, want) {
				t.Fatalf("services=%v want=%v", got, want)
			}
		})
	}
}
