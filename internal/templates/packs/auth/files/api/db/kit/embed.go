// Package kit embeds the migrations that spinup ships.
package kit

import "embed"

// FS contains the SQL migrations.
//
//go:embed *.sql
var FS embed.FS
