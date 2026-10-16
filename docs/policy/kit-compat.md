# Kit compatibility policy

The kit is the runtime library of each generated app.
The CLI and the kit share one major and minor version.
The kit stays on v0.x until two minors pass with no breaking change.
A machine check enforces this rule.

Check the version of the CLI, which is also the kit version:

```console
$ spinup version
...
```

## What the guard covers

The package `internal/apicompat` reads the source of the kit.
It lists each exported identifier of each kit package.
It writes one line for each identifier.
The lines are sorted, so the output is deterministic.

The guard does not cover these paths:

- `kit/internal/...` is private code.
- `kit/x/...` is unstable by policy.
- `kit/devtools` serves the `/_dev` page. `kit/app` mounts it at run time only when `SPINUP_ENV=dev`.

A line has the form `<package> <kind> <name><signature>`.
The kinds are `func`, `method`, `ptrmethod`, `type`, `field`, `imethod`, `const`, and `var`.
Parameter names are not part of the line.
A rename of a parameter is not a breaking change.

The committed baseline is `kit/api/current.txt`.
The test `TestKitAPI` compares the baseline with the source.

| Difference | Result |
|---|---|
| A line is removed or changed | The test fails. |
| A line is added | The test fails until you update the baseline. |
| A line only gains a deprecation marker | The test fails until you update the baseline. |
| A deprecated line is removed and the deprecation is one minor old | The test passes. |

## Add API

1. Write the code and the tests.
2. Run `SPINUP_GOLDEN_UPDATE=1 go test ./internal/apicompat`.
3. Review the new lines in `kit/api/current.txt`.
4. Commit the baseline with the code.

The update command refuses to rewrite the baseline over a breaking change.
Set `SPINUP_API_BREAK=1` only when the release plan allows the break.

## Deprecate API

Write the comment on its own paragraph in the doc comment.
Use this exact form on one line:

```go
// Deprecated: since v0.9. Use NewThing instead.
```

Use `No replacement.` in place of `Use X instead.` when there is no successor.
`TestKitDeprecations` checks every `Deprecated:` line in the kit.
The test checks all kit code, including `kit/internal` and `kit/x`.

If the function body is one return or one call of another function, add `//go:fix inline`.
Put the directive on the last line of the doc comment, after a blank comment line:

```go
// Old returns a thing.
//
// Deprecated: since v0.9. Use New instead.
//
//go:fix inline
func Old() *Thing { return New() }
```

Make the deprecated code path call `deprecation.Warn`.
The helper logs one warning for each key in each process:

```go
deprecation.Warn(logger, "pkg.Old", "pkg.Old is deprecated since v0.9. Use pkg.New instead.")
```

Then run the update command.
The baseline line gets a marker such as `deprecated:v0.9`.

## Remove API

Keep the deprecated API for one minor.
The baseline marker says `deprecated:v0.N`.
You can remove the API when the current kit minor is greater than N.
Removing a deprecated type also allows the removal of its fields and methods.
After the removal, run the update command again.

The current kit minor is in `kit/api/minor.txt`.
The release process changes this file.
See `docs/contributing/release.md`.

## Check the guard

Run these commands from the repository root:

```sh
go test -race ./internal/apicompat/...
cd kit && go test -race ./deprecation/...
```
