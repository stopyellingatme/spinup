# Generate a mailer

Run `spinup g mailer <name> [field:type...]` from the application root.
The command also accepts the name `spinup generate mailer`.
The application must include the `mail` module.

<!-- doctest: only go -->
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

The Rust API gives these results:

<!-- doctest: only rust -->
```console
$ spinup new todo --api-only --module example.com/todo --modules ci,mail --no-git --non-interactive
...
$ cd todo
$ spinup g mailer welcome name:string --dry-run
update api/src/mail/mod.rs (managed)
create api/src/mail/welcome.rs (managed)
create api/templates/mail/welcome.html.tmpl (seed)
create api/templates/mail/welcome.subject.tmpl (seed)
create api/templates/mail/welcome.txt.tmpl (seed)
$ spinup g mailer welcome name:string
Created 4, updated 1 files
Next: cd api && cargo test --locked mail
$ cat api/templates/mail/welcome.txt.tmpl
Hello {{ name }}
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
Run that command, or create the application with `--modules ci,mail`.

<!-- doctest: only rust -->
```console
$ spinup new plain --api-only --module example.com/plain --no-git --non-interactive
...
$ cd plain
$ spinup g mailer welcome name:string
Error: generator "mailer" failed: cannot generate a mailer: the app has no mail module
  Try: spinup module add mail
[exit 1]
```

## Rust API

In a Rust API, `spinup g mailer <name>` writes three seed templates in `api/templates/mail/`.
The templates use minijinja syntax, for example `{{ name }}`.
It also writes the managed file `api/src/mail/<name>.rs` and rewrites the managed module list `api/src/mail/mod.rs`.
The generator finds the existing mailers by their file names.
It does not parse the Rust code.

The managed file has the `<Name>Data` type and a `send_<name>` method on `Mailers`:

```rust
let data = crate::mail::welcome::WelcomeData { name: "Reader".into() };
mailers.send_welcome("reader@example.com", &data).await?;
```

With the `jobs` module, the `mail.send` job delivers the email.
The application binary runs that job with `api worker`.
