---
title: generate flags — declare feature flags
parent: Command guides
---

# generate flags — declare feature flags

Declare each flag in `spinup.toml`. The app needs the `flags` module.

<!-- doctest: skip -->
```toml
[flags.new_checkout]
description = "The new checkout flow"
public = true
expires = "2027-03-31"
```

Run `spinup g flags` after you change the declarations.
The command has no arguments or flags.
It writes Go constants and a TypeScript union of public keys.
It also writes the administrator role from `[auth]`.

<!-- doctest: skip -->
```console
$ spinup g flags
```

The `flags-drift` doctor check warns if either generated file differs from the declarations.
It also warns when a flag has passed its expiry date.
Remove expired flags from the configuration and code, then run `spinup g flags` and `flags prune --yes`.
