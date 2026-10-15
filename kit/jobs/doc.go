// Package jobs runs background jobs on River (github.com/riverqueue/river)
// with PostgreSQL.
//
// An app inserts jobs with a [Client] (from [New]) and works them in a
// separate worker process with a [Worker] (from [NewWorker]). A job is an
// [Args] value: a JSON-encodable struct with a Kind method. [Register] adds
// the function that works one kind.
//
//	w, err := jobs.NewWorker(pool)
//	jobs.Register(w, func(ctx context.Context, job jobs.Job[SendDigestArgs]) error { ... })
//	w.Periodic(time.Hour, SendDigestArgs{})
//	err = w.Start(ctx)
//	defer w.Stop(ctx)
//
// River keeps its tables in the app database. [MigrationHook] adds the River
// migrations to the app's `migrate up` step (kit db.WithHook). [MigrateCommand]
// is the explicit `jobs-migrate up | down | status` command.
//
// River types stay inside this package, so an app depends only on the kit.
package jobs
