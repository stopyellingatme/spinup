// Package backup reports backup state from inside the app.
//
// LastBackupAge returns the age of the newest database backup. Handler serves it at
// /_deploy/backup as {"last_backup_age_seconds": N}. The package uses no metrics library.
// The monitoring milestone maps the value to an OpenTelemetry gauge, and alerts use it.
package backup
