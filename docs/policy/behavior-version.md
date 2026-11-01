---
title: Behavior versions
parent: Reference
nav_order: 10
---

# Behavior versions

The behavior version lets the kit change a default without a surprise.
When you upgrade spinup, your app keeps its old behavior. You switch to the new behavior when you are ready.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ grep behavior_version todo/spinup.toml
behavior_version = 1
```

## What the number means

`behavior_version` is a whole number in `spinup.toml`. A new app starts at `1`.
A higher number includes every behavior of the lower numbers, plus the new ones of its own.

The number is not the kit version. The kit can ship many minors and keep the same highest behavior version.
The number goes up only when the kit changes a default in a way that your app can see.

## Raise it for your app

1. Run `spinup upgrade`. It never changes `behavior_version`.
2. Look for new behavior versions. `spinup upgrade --plan` prints a `Kit behavior` line for each one that is higher than yours. The changelog gives details.
3. Set `behavior_version` in `spinup.toml` to the new number.
4. Run the tests and `spinup dev`.
5. Run `spinup deploy`. The deploy sends the new number to the server.

<!-- doctest: skip -->
```console
$ spinup upgrade --plan
Upgrade 0.4.3 -> 0.5.1
...
Kit behavior 2 (opt in with behavior_version = 2 in spinup.toml): <what changes>
```

```toml
behavior_version = 2
```

The versions and the description in the plan above are placeholders. Your plan shows the real ones. If the plan has no `Kit behavior` line, nothing is new.

To go back, set the old number and deploy again.
`spinup.toml` is a scaffold file, so an upgrade keeps your value.

## How the app receives the number

The CLI reads `behavior_version` and passes it to the app as the variable `SPINUP_BEHAVIOR_VERSION`. A missing value or a value below 1 becomes 1.

| Where | How the value arrives |
| --- | --- |
| `spinup dev` | The dev loop sets the variable for the API and its migrations. |
| `spinup deploy` | The deploy writes it into the release `.env` file. |
| Production Compose | `deploy/compose.yml` passes it to the `api` and `migrate` services. |
| Your own run of the binary | Set `SPINUP_BEHAVIOR_VERSION` yourself. Without it, the kit uses 1. |

## The rule

A new behavior never changes an existing app without the opt-in. In practice:

- A changed default ships behind a new behavior version. The defaults of the lower numbers never change.
- A bug fix is not a behavior change. A security fix applies to every behavior version.
- The old path stays until the deprecation window ends. See [Support policy](support.md).
- The changelog lists each new behavior version with the old and the new behavior.

## For kit contributors

Gate a new behavior in kit code with the `config.Base` value of the app. `Behavior(v)` is true when the app has opted in to version `v` or higher.

```go
if base.Behavior(2) {
	// New default. Only apps with behavior_version 2 or higher run this.
} else {
	// Old default. Keep it until a later behavior version removes it.
}
```

- Use the number of the release that adds the behavior.
- Read the number only through `config.Base.Behavior(v)`. Do not read the environment variable in other code.
- Write a test for each side of the gate.
- Add the new version to the `behaviors` list in `kit/MIGRATION.json`, with a `version` and a `desc`. `spinup upgrade --plan` shows the `desc` to apps that have not opted in.

## See also

- [spinup.toml](../reference/configuration.md): where `behavior_version` sits
- [Kit compatibility](kit-compat.md): the promise about kit changes
- [Support policy](support.md): supported versions and deprecation
- [Upgrade](../upgrading.md): move an app to a new version
