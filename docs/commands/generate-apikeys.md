---
title: generate apikeys — declare API key plans
parent: Command guides
---

# generate apikeys — declare API key plans

Declare plans in `spinup.toml`. The app needs the `apikeys` module.

<!-- doctest: skip -->
```toml
[apikeys]
prefix = "key"
default_plan = "free"
max_keys = 10

[apikeys.plans.free]
per_minute = 60
per_day = 1000
```

Run `spinup g apikeys` after you change the declarations.
The command has no arguments or flags.
It writes the Go configuration and the TypeScript administrator role.

```console
$ spinup new keyapp --module example.com/keyapp --modules ci,auth,apikeys --no-git --non-interactive
...
$ cd keyapp
$ printf '\n[apikeys]\nprefix = "example"\n' >> spinup.toml
$ spinup g apikeys
Created 0, updated 1 files
$ grep -n "example" api/internal/apikeys/apikeys.gen.go
...
$ cat web/src/lib/apikeys.gen.ts
...
```

If nothing changed, the command writes nothing.

```console
$ spinup g apikeys
Created 0, updated 0 files
```

The `apikeys-drift` doctor check warns if either generated file differs from the declarations.
