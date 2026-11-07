// Package db opens PostgreSQL pools and runs goose migrations.
//
// [Open] builds a pgx pool with safe runtime defaults. [InTx] runs a function
// in a transaction. [Migrator] and [MigrateCommand] run plain SQL migrations
// through goose providers with a Postgres advisory lock. The lock serializes
// migration runners.
//
// [WithKitMigrations] adds a kit space with its own [KitVersionTable].
// Legacy kit rows move from the app table after checks of their schema objects.
// [Migrator.Status] and [Migrator.Pending] describe the app space.
//
// [MigrateCommand] runs up, down, status, redo, to <version>, and version.
// Up applies the kit space first. Status lists the kit files first.
// The other subcommands operate on the app space.
// The kit subcommand runs up, down, status, to <version>, and version for kit files.
// [WithHook] adds a [Hook] that runs extra migrations in the same step (for
// example a job queue): AfterUp runs after the app migrations move up, and
// BeforeDown runs before they move down.
package db
