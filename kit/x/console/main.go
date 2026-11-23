package console

import (
	"fmt"
	"io"
	"slices"
)

// Main executes a console command and returns its process exit code.
func Main(args []string, _ io.Writer, stderr io.Writer) int {
	valid := len(args) == 1 && slices.Contains([]string{"serve", "query", "project", "healthcheck"}, args[0]) ||
		len(args) == 2 && args[0] == "healthcheck" && args[1] == "query"
	if !valid {
		_, _ = fmt.Fprintln(stderr, "Cannot run console: the arguments are invalid.\nTry: use serve, query, project, healthcheck, or healthcheck query.")
		return 2
	}
	_, _ = fmt.Fprintln(stderr, "Cannot run console: the command implementation is unavailable.\nTry: deploy a console image with the command implementation.")
	return 1
}
