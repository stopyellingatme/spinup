# Generate the TypeScript client

Run `spinup g client` from the application root after an OpenAPI change.
The command also accepts `spinup generate client`.
It takes no positional arguments.

The generator returns an empty plan.
Its post-steps first bundle OpenAPI and generate Go types.
Then the client post-step generates `web/src/lib/api/schema.d.ts`.
Commit this file with the OpenAPI bundle.
The lock does not track changes to this tool output.

The client post-step runs this command from the application root:

```sh
pnpm --dir web exec openapi-typescript ../api/openapi/bundle.gen.yaml -o src/lib/api/schema.d.ts
```

If `web/node_modules` is missing, the post-step first runs `pnpm --dir web install --frozen-lockfile`.
If the web pack or the bundle is absent, it prints a skip reason on stderr.
If pnpm is unavailable, it reports an error with a `Try:` instruction.

Use `--dry-run` or `--diff` to preview without writing files or running tools.
Use `--json` for structured results.
After generation, run `cd web && pnpm check`.

This example verifies the preview and the skip for an application without the web pack:

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g client --dry-run
$ test ! -e api/openapi/bundle.gen.yaml
$ spinup g client
no OpenAPI paths; oapi-codegen skipped
Try: spinup g resource <name> <field:type>...
client generation skipped: no web pack
Created 0, updated 0 files
Next: cd web && pnpm check
$ test -s api/openapi/bundle.gen.yaml
$ test ! -e web/src/lib/api/schema.d.ts
```

The `client-drift` check runs during a full local `spinup doctor` check.
It generates types into a temporary file and compares them with `schema.d.ts`.
The comparison normalizes CRLF line endings to LF.
The check leaves the application schema unchanged and removes its temporary output.

If the types differ or the schema is missing, the check warns with `Try: spinup g client`.
It skips outside a project, without the web pack, without a bundle, or without `web/node_modules`.
It also skips during `doctor --quick`.
