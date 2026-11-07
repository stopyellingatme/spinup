package mcp

import (
	"github.com/jackc/pgx/v5/pgxpool"
	kitmcp "github.com/stopyellingatme/spinup/kit/x/mcp"
)

// Deps holds the application dependencies for MCP tools.
type Deps struct {
	DB      *pgxpool.Pool
	Version string
}

// New creates the MCP server and registers the application tools.
func New(d Deps) (*kitmcp.Server, error) {
	s, err := kitmcp.New(kitmcp.Config{Name: "todo", Version: d.Version})
	if err != nil {
		return nil, err
	}
	registerWhoami(s)
	registerGenerated(s, d)
	return s, nil
}
