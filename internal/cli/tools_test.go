package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestToolsRegistered(t *testing.T) {
	t.Parallel()
	var entered bool
	app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, &bytes.Buffer{}, &bytes.Buffer{}, ui.Options{})}
	root := newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)
	command, _, err := root.Find([]string{"tools", "install"})
	if err != nil || command.Name() != "install" {
		t.Fatalf("tools install command = %v, %v", command, err)
	}
	if command.Flags().Lookup("force") == nil {
		t.Fatal("tools install has no --force flag")
	}
}
