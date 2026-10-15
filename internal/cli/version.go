package cli

import (
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/selfupdate"
)

// Build information. GoReleaser sets these with -ldflags -X.
var (
	// Version is the CLI version. It is also the kit version.
	Version = "dev"
	// Commit is the git commit of the build.
	Commit = "none"
	// Date is the build date.
	Date = "unknown"
)

type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	Kit     string `json:"kit"`
}

// currentVersion returns the build information. If Version is "dev", it reads
// the module data that the Go toolchain embeds in the binary.
func currentVersion() versionInfo {
	v := versionInfo{Version: Version, Commit: Commit, Date: Date, Go: runtime.Version()}
	if Version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			if mv := bi.Main.Version; mv != "" && mv != "(devel)" {
				v.Version = mv
			}
			for _, s := range bi.Settings {
				switch {
				case s.Key == "vcs.revision" && v.Commit == "none" && s.Value != "":
					v.Commit = s.Value
				case s.Key == "vcs.time" && v.Date == "unknown" && s.Value != "":
					v.Date = s.Value
				}
			}
		}
	}
	v.Kit = v.Version
	return v
}

func init() {
	Register(GroupMaintain, func(app *App) *cobra.Command {
		var check bool
		command := &cobra.Command{
			Use:   "version",
			Short: "Print the spinup version",
			Args:  cobra.NoArgs,
			RunE: func(c *cobra.Command, _ []string) error {
				v := currentVersion()
				if check {
					if err := updateOnline(app); err != nil {
						return err
					}
					result, err := (selfupdate.Updater{}).Check(c.Context(), selfupdate.Options{Current: v.Version})
					if err != nil {
						return err
					}
					return printUpdateResult(app, result)
				}
				if app.Flags.JSON {
					return app.UI.JSON(v)
				}
				app.UI.Printf("spinup %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.Date, v.Go)
				return nil
			},
		}
		command.Flags().BoolVar(&check, "check", false, "check for a newer stable release")
		return command
	})
}
