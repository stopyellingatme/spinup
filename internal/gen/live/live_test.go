package live

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func TestMarkers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	empty, err := RenderGenerated(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := os.ReadFile("../../templates/packs/live/files/api/internal/live/feeds_gen.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(empty.Data, pack) {
		t.Fatalf("empty registrations differ:\n%s", empty.Data)
	}
	pending := []vfs.File{{Path: Dir + "/z.go", Data: []byte("package live\n// spinup:live-feed z registerZFeed\n")}, {Path: Dir + "/a.go", Data: []byte("package live\n// spinup:live-feed a registerAFeed\n")}}
	feeds, err := ScanFeeds(root, pending)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 2 || feeds[0].Name != "a" || feeds[1].Name != "z" {
		t.Fatalf("feeds: %+v", feeds)
	}
	generated, err := RenderGenerated(root, pending)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(generated.Data), "registerAFeed(mux, h, d)") > strings.Index(string(generated.Data), "registerZFeed(mux, h, d)") {
		t.Fatal("registration order")
	}
	for _, marker := range []string{"a registerZFeed", "z registerAFeed"} {
		duplicate := append(append([]vfs.File{}, pending...), vfs.File{Path: Dir + "/duplicate.go", Data: []byte("package live\n// spinup:live-feed " + marker + "\n")})
		if _, err := ScanFeeds(root, duplicate); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("duplicate: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, Dir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, Dir, "a.go"), []byte("broken Go"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanFeeds(root, nil); err == nil {
		t.Fatal("invalid Go accepted")
	}
	if _, err := ScanFeeds(root, pending); err != nil {
		t.Fatal("pending did not replace disk:", err)
	}
}

func TestFeedSource(t *testing.T) {
	t.Parallel()
	name, err := fields.ParseName("note")
	if err != nil {
		t.Fatal(err)
	}
	owned, err := FeedSource(FeedSpec{Module: "example.com/demo/api", Name: name, Owned: true})
	if err != nil {
		t.Fatal(err)
	}
	golden := "../resource/testdata/live_feed.golden"
	if os.Getenv("SPINUP_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(golden, owned, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(owned, want) {
		t.Fatalf("feed differs from %s:\n%s", golden, owned)
	}
	shared, err := FeedSource(FeedSpec{Module: "example.com/demo/api", Name: name, Shared: true, Roles: []string{"manager", "staff"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"kitlive.SharedKey(notesFeed)", "user.HasRole(roles.Manager, roles.Staff)", "kitlive.ErrForbidden", "gen.ListNotes200JSONResponse", "int32(min(limit, 200))"} {
		if !strings.Contains(string(shared), part) {
			t.Errorf("missing %s", part)
		}
	}
}
