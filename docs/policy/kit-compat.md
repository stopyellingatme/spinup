---
title: Kit compatibility
parent: Reference
nav_order: 9
---

# Kit compatibility

The kit is the Go library that your app imports from `api/cmd/api/main.go` and your handlers.
This page says how spinup keeps the kit from breaking your app, and what to do when a kit API is deprecated.
The last section is for people who change the kit itself.

```go
// A deprecated API shows this note in your editor, with the replacement.
//
// Deprecated: since v0.9. Use NewThing instead.
func OldThing() *Thing { return NewThing() }
```

## The promise

- The CLI and the kit share one major and minor version. Patch versions can differ.
- Until version 1.0, the kit stays on v0.x. Version 1.0 needs two minors in a row with no breaking change.
- A kit API is deprecated for one full minor before it is removed.
- A new default never changes your app on its own. You opt in with [`behavior_version`](behavior-version.md).

A machine check in the spinup repository enforces the rules. A kit change that removes or alters an API fails the check until the change follows the steps below.
Code under `kit/internal/` is private. It can change at any time. Do not import it.

## Check that your kit matches

`spinup doctor` compares the kit version in `api/go.mod` with the CLI version and warns when the major or minor differ.

```console
$ spinup doctor --help
Check local application health
...
```

Run `spinup upgrade` to move the kit and the templates together. See [Upgrade](../upgrading.md).
Or run `go get github.com/stopyellingatme/spinup/kit@vX.Y.x` in `api/` with your CLI version.

## When an API is deprecated

A deprecated API still works for one minor. You see it in three places:

1. Your editor (through `gopls`) and `staticcheck` mark the call and show the `Deprecated:` note with the replacement.
2. The app logs one warning at startup, such as `pkg.Old is deprecated since v0.9. Use pkg.New instead.`
3. The [changelog](https://github.com/stopyellingatme/spinup/blob/main/CHANGELOG.md) lists it under "Breaking" or "Upgrade steps".

For a simple forwarding function, the kit adds `//go:fix inline`. Then `go fix ./...` replaces the old call with the new one for you:

```sh
cd api
go fix ./...
```

Replace each deprecated call before you upgrade to the next minor. The next minor can remove the API, and your build then fails.

## For kit contributors

Use this part when you change a package under `kit/` in the spinup repository.
The file `kit/api/current.txt` lists every exported name of the kit. A test compares it with the source.

A line has the form `<package> <kind> <name><signature>`:

```text
kit/auth func New(*pgxpool.Pool, ...Option) (*Auth, error)
kit/auth ptrmethod Auth.RequireRole(...string) func(http.Handler) http.Handler
kit/config field Base.Port int
```

Parameter names are not part of a line, so you can rename a parameter. The guard skips `kit/internal/` and `kit/x/`.

| Difference | Result |
| --- | --- |
| A line is removed or changed | The test fails. |
| A line is added | The test fails until you update the baseline. |
| A line gains a deprecation marker | The test fails until you update the baseline. |
| A deprecated line is removed one minor after its marker | The test passes. |

### Add an API

1. Write the code and the tests.
2. Run `SPINUP_GOLDEN_UPDATE=1 go test ./internal/apicompat`.
3. Read the new lines in `kit/api/current.txt`.
4. Commit the baseline with the code.

The update refuses to rewrite the baseline over a breaking change. Set `SPINUP_API_BREAK=1` only when the release plan allows the break.

If the guard fails, the message names what changed:

```text
removed from the kit API (breaking):
  kit/auth func Old()
Try: restore the removed or changed API, or deprecate it with `Deprecated: since v0.N. Use X instead.` and remove it one minor later.
```

### Deprecate an API

Write the note as its own paragraph of the doc comment, on one line:

```go
// Old returns a thing.
//
// Deprecated: since v0.9. Use New instead.
//
//go:fix inline
func Old() *Thing { return New() }
```

- Write `No replacement.` in place of `Use X instead.` when there is no successor.
- Add `//go:fix inline` as the last line when the body is one return or one call.
- Call `deprecation.Warn(logger, "pkg.Old", "pkg.Old is deprecated since v0.9. Use pkg.New instead.")` on the old path. It logs one warning for each key in each process.
- Run the update command. The baseline line gets a marker such as `deprecated:v0.9`.

### Remove an API

Keep the deprecated API for one minor. You can remove it when the current kit minor is higher than the minor in the marker.
The current minor is in `kit/api/minor.txt`, which holds `0.1` today. The release process changes it.
Removing a deprecated type also allows removing its fields and methods. Run the update command again afterward.

### Run the guard

From the root of the spinup repository:

```sh
go test -race ./internal/apicompat/...
cd kit && go test -race ./deprecation/...
```

## See also

- [Behavior versions](behavior-version.md): how the kit changes a default without a surprise
- [Support policy](support.md): supported versions and the upgrade path
- [The kit library](../reference/kit.md): the packages and their use
- [Upgrade](../upgrading.md): move an app to a new version
