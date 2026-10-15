package ops

import (
	"context"
	"fmt"
	"path/filepath"

	run "github.com/stopyellingatme/spinup/internal/exec"
)

// QueriesDir is the sqlc query directory, relative to the project root.
const QueriesDir = "api/db/queries"

// Sqlc runs `go tool -modfile=tools.mod sqlc generate` in api/. When
// api/db/queries holds no .sql file, sqlc would fail, so Sqlc says so and
// returns nil.
func Sqlc(ctx context.Context, o Options, runner run.Runner) error {
	const what = "Cannot generate the query code"
	root, _, err := loadProject(o, what)
	if err != nil {
		return err
	}
	matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(QueriesDir), "*.sql"))
	if err != nil {
		return opError(what, err)
	}
	if len(matches) == 0 {
		_, err := fmt.Fprintf(o.Out, "No queries in %s, so sqlc has nothing to generate.\nTry: spinup g resource <name> <field:type>...\n", QueriesDir)
		return err
	}
	c := run.Cmd{Name: "go", Args: []string{"tool", "-modfile=tools.mod", "sqlc", "generate"}, Dir: filepath.Join(root, "api")}
	return runStreaming(ctx, o, runner, what, c)
}
