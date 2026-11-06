// Package migrations embeds the app migrations.
package migrations

import "embed"

// FS contains the app migrations.
//
//go:embed *.sql
var FS embed.FS
