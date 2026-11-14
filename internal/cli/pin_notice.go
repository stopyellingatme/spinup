package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/project"
)

func printPinNotice(c *cobra.Command, app *App, errOut io.Writer) {
	if app.Flags.JSON || pinNoticeDisabled(c) || os.Getenv("SPINUP_NO_PIN_NOTICE") == "1" {
		return
	}
	if _, ok := os.LookupEnv("CI"); ok {
		return
	}
	dir := app.Flags.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(root, project.LockFile))
	if err != nil {
		return
	}
	var lock struct {
		CLIVersion string `json:"cli_version"`
	}
	if err := json.Unmarshal(data, &lock); err != nil || lock.CLIVersion == "" {
		return
	}
	advice := compat.CheckPin(Version, lock.CLIVersion)
	if advice.State == compat.PinCLIOlder || advice.State == compat.PinCLINewer {
		_, _ = fmt.Fprintf(errOut, "Warning: %s. Try: %s\n", advice.Message, advice.Try)
	}
}

func pinNoticeDisabled(c *cobra.Command) bool {
	top := c
	for top.Parent() != nil && top.Parent().Parent() != nil {
		top = top.Parent()
	}
	switch top.Name() {
	case "new", "upgrade", "self-update", "version", "doctor", "docs", "completion", "help":
		return true
	default:
		return false
	}
}
