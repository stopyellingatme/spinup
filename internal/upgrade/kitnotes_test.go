package upgrade

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestPrintKitNotesShowsOnlyNewerBehaviors(t *testing.T) {
	to := &templates.Version{Version: "0.3.0", KitMigration: []byte(`{"schema":1,"version":"0.3.0","steps":[],` +
		`"behaviors":[{"version":2,"desc":"already selected"},{"version":3,"desc":"JSON errors v2"}]}`)}
	var out bytes.Buffer
	if err := printKitNotes(&out, to, 2); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "already selected") || !strings.Contains(out.String(), "Kit behavior 3 (opt in with behavior_version = 3 in spinup.toml): JSON errors v2") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestPrintKitNotesRejectsBadBehaviors(t *testing.T) {
	for _, notes := range []string{
		`{"schema":1,"version":"0.3.0","steps":[],"behaviors":[{"version":1,"desc":"x"}]}`,
		`{"schema":1,"version":"0.3.0","steps":[],"behaviors":[{"version":2,"desc":"x"},{"version":2,"desc":"y"}]}`,
		`{"schema":1,"version":"0.3.0","steps":[],"behaviors":[{"version":2,"desc":" "}]}`,
	} {
		err := printKitNotes(&bytes.Buffer{}, &templates.Version{Version: "0.3.0", KitMigration: []byte(notes)}, 1)
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("notes %s: error = %v", notes, err)
		}
	}
}
