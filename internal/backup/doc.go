// Package backup backs up the app database on the VM.
//
// Backend is the interface of a backup backend. PGBackRest is the default backend (ADR 0004):
// it runs pgbackrest in the postgres container of the base Compose project through
// remote.Runner, and parses `pgbackrest info --output=json`. Now, List, and ShowStatus are
// the operations of `spinup backup`. Hook is the pre-deploy hook named "backup", which takes a
// diff backup before every deploy.
package backup
