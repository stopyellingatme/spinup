---
title: generate billing — declare billing plans
parent: Command guides
---

# generate billing — declare billing plans

Declare plans in `spinup.toml`. The app needs the `billing` module.

<!-- doctest: skip -->
```toml
[billing]
default_plan = "free"

[[billing.plans]]
name = "free"

[[billing.plans]]
name = "pro"
prices = ["pro_monthly"]
features = ["reports"]
```

Run `spinup g billing` after you change the declarations.
The command has no arguments or flags of its own.
It renders the managed Go and TypeScript files.

| Inherited flag | Effect |
| --- | --- |
| `--dry-run` | Show the planned changes without writing files. |
| `--diff` | Show the changes as diffs without writing files. |
| `--force` | Permit replacement of files with different content. |
| `--json` | Print the result as JSON. |
| `--dir`, `-C` | Select the project directory. |
| `--env` | Select the target environment. |
| `--yes`, `-y` | Answer yes to prompts. |
| `--confirm-env` | Confirm the protected environment. |
| `--non-interactive` | Disable prompts. |
| `--verbose` | Print extra detail. |
| `--no-color` | Disable color. |
| `--offline` | Disable template downloads. |
| `--help`, `-h` | Show help for the command. |

The `billing-drift` doctor check warns if either generated file differs from the declarations.

```console
$ spinup new billingapp --module example.com/billingapp --modules auth,jobs,billing --no-git --non-interactive
...
$ cd billingapp
$ spinup g billing
Created 0, updated 0 files
```
