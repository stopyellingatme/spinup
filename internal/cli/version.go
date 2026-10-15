package cli

import (
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
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
		return &cobra.Command{
			Use:   "version",
			Short: "Print the spinup version",
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				v := currentVersion()
				if app.Flags.JSON {
					return app.UI.JSON(v)
				}
				app.UI.Printf("spinup %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.Date, v.Go)
				return nil
			},
		}
	})
}
