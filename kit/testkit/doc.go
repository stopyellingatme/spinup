// Package testkit gives tests a PostgreSQL database and HTTP helpers.
//
// [DB] returns a pool on a new, empty database. [DBWithMigrations] returns a
// pool on a clone of a migrated template database. Both drop the database
// when the test ends.
//
// The server is the one in DATABASE_URL when that variable is set. The URL
// must name a role that can create databases. Otherwise testkit starts one
// PostgreSQL container for the test process with testcontainers. When neither
// is available, the test skips, or fails when the CI variable is set.
package testkit
