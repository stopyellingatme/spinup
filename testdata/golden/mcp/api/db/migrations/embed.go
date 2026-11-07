// Package migrations embeds the application migrations.
package migrations

import "embed"

// FS contains the SQL migrations.
//
//go:embed *.sql
var FS embed.FS
