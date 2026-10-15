# Generate a mailer

Run `spinup g mailer <name> [field:type...]` from the application root.
The application must include the `mail` module.

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,mail --no-git --non-interactive
...
$ cd todo
$ spinup g mailer welcome name:string --dry-run
create api/internal/mail/templates/welcome.html.tmpl (seed)
create api/internal/mail/templates/welcome.subject.tmpl (seed)
create api/internal/mail/templates/welcome.txt.tmpl (seed)
create api/internal/mail/welcome_gen.go (managed)
$ spinup g mailer welcome name:string
Created 4, updated 0 files
Next: cd api && go test ./internal/mail/...
```

The generator returns three seed templates and one managed Go file.
The templates use Go template syntax.
The HTML template escapes data.
Every email includes a text part.

The generated method accepts typed data:

```go
err := mailers.SendWelcome(ctx, "reader@example.com", mail.WelcomeData{Name: "Reader"})
```

Edit the templates to change the email content.
Use the shared field types, including `string`, `int`, `bool`, `time`, `uuid`, and nullable types.
Use `--dry-run` to inspect the plan.
Use `--diff` to inspect changes.

If the application has no mail module, the error includes `Try: spinup module add mail`.
Create an application with `--modules ci,mail` when that command is unavailable.
