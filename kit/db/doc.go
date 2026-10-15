// Package db opens PostgreSQL pools and runs goose migrations.
//
// [Open] builds a pgx pool with safe runtime defaults. [InTx] runs a function
// in a transaction. [Migrator] and [MigrateCommand] run plain SQL migrations
// through a goose provider that holds a Postgres advisory lock, so two runners
// never migrate the same database at once.
//
// [MigrateCommand] runs up, down, status, redo, to <version>, and version.
// [WithHook] adds a [Hook] that runs extra migrations in the same step (for
// example a job queue): AfterUp runs after the app migrations move up, and
// BeforeDown runs before they move down.
package db
