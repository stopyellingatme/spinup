---
title: generate mailer — add an email template
parent: Command guides
---

# generate mailer — add an email template

`spinup g mailer <name> [field:type...]` adds one email to your app.
It writes the subject, text, and HTML templates, and a typed Go method that renders and sends them.
Use it for each email your app sends, such as a welcome message.

The app needs the `mail` module.
Create the app with `--modules ci,mail`, or add it later with `spinup module add mail`.

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

Each field becomes a value that the templates can use.
The fields use the same types as [`g resource`](generate-resource.md#name-the-resource-and-its-fields).
A `?` after the type makes the field optional, as in `'plan:string?'`.

## What it writes

| File | Class | What it is |
| --- | --- | --- |
| `api/internal/mail/templates/<name>.subject.tmpl` | seed | The subject line. |
| `api/internal/mail/templates/<name>.txt.tmpl` | seed | The plain text body. Every email has a text part. |
| `api/internal/mail/templates/<name>.html.tmpl` | seed | The HTML body. Go escapes the data for you. |
| `api/internal/mail/<name>_gen.go` | managed | The `<Name>Data` type and the `Send<Name>` method. Do not edit it. |

## Send the email

`api/cmd/api/main.go` already builds the `mailers` value.
Pass it to your handlers, then call the typed method:

```go
err := mailers.SendWelcome(ctx, "reader@example.com", mailapp.WelcomeData{Name: "Reader"})
```

The data type has one field for each field that you gave.
An optional field is a pointer, so `plan:string?` becomes `Plan *string`.

In development, mail goes to Mailpit at `http://localhost:8025`.
In other environments, set `SMTP_URL`.
See [Mail](../mail.md) for the settings and for testing with the capture mailer.

## Edit the templates

The templates are seed files: they start as a short draft, and you own them from then on.
Edit the text, the HTML, and the subject.
A template uses the fields as `{{.Name}}`.

```text
Hello {{.Name}},

Welcome to the todo app.
```

## Add a field later

If you run the generator again with other fields, it stops, because your templates differ from the new draft.
This protects your edits.

```console
$ spinup g mailer welcome name:string 'plan:string?' --diff
Error: generator "mailer" would overwrite existing files with different content:
  api/internal/mail/templates/welcome.html.tmpl
  api/internal/mail/templates/welcome.txt.tmpl
  Try: pass --force, or remove the files
[exit 1]
```

{: .warning }
`--force` replaces the templates with new drafts. Copy your edited templates first, run the command with `--force`, then restore your text.

## Troubleshooting

If the app has no `mail` module, the command fails and names the fix:

```console
$ spinup new plain --api-only --module example.com/plain --no-git --non-interactive
...
$ cd plain
$ spinup g mailer welcome name:string
Error: generator "mailer" failed: cannot generate a mailer: the app has no mail module
  Try: spinup module add mail
[exit 1]
```

## See also

- [Mail](../mail.md): settings, Mailpit, and password reset emails
- [generate](generate.md): preview and overwrite rules
- [module](module.md): `spinup module add mail`
- Flag reference: [`generate mailer`](../site/commands/generate-mailer.md)
