package migrate

import "github.com/stopyellingatme/spinup/internal/stackversions"

// SvMigrateWith builds the sv migrate codemod with fixed stack versions, because the
// migratetest harness does not set Ctx.Old and Ctx.New.
func SvMigrateWith(old, next *stackversions.Stack) Migration {
	return svMigrate(func(*Ctx) (*stackversions.Stack, *stackversions.Stack) { return old, next })
}
