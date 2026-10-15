# Behavior version policy

The behavior version lets the kit change a default without a surprise.
A kit upgrade never changes the behavior of an existing app.
An app opts in to new behavior on purpose.

## What the number means

`behavior_version` is an integer in `spinup.toml`.
A new app starts with `behavior_version = 1`.
The kit defines what each number selects.
A higher number selects every behavior of the lower numbers, and the new behaviors of its own.

The number is not a kit version.
The kit can ship many minors with no change to the highest behavior version.
The number changes only when the kit changes a default in a way that an app can see.

## How the app receives the number

The CLI reads `behavior_version` from `spinup.toml`.
It passes the value to the app as the environment variable `SPINUP_BEHAVIOR_VERSION`.
If the key is missing or lower than 1, the CLI passes 1.

| Place | How the value arrives |
| --- | --- |
| `spinup dev` | The dev loop adds `SPINUP_BEHAVIOR_VERSION` to the environment of the API and its migration step. |
| `spinup deploy` | The deployer writes `SPINUP_BEHAVIOR_VERSION` to the release `.env` file. |
| Production Compose | `deploy/compose.yml` passes the value to the `api` and `migrate` services. |

If you run the binary yourself, set `SPINUP_BEHAVIOR_VERSION` in the environment.
If you do not set it, `kit/config` uses 1.

## How an app opts in

1. Run `spinup upgrade`. It does not change `behavior_version`.
2. Read the entry for each new behavior version in `CHANGELOG.md`.
3. Change `behavior_version` in `spinup.toml` to the new number.
4. Run the tests and `spinup dev`.
5. Run `spinup deploy`. The deployer writes the new value to the release.

To go back, set the old number again and deploy again.
`spinup upgrade` never changes `behavior_version`.
`spinup.toml` is a scaffold file, so an upgrade keeps your value.

## How kit code gates behavior

Kit code reads the number from the `config.Base` value of the app configuration.
Use `Behavior(v)` to ask if the app opted in to version `v`.

```go
if base.Behavior(2) {
	// New default. Only apps with behavior_version 2 or higher run this code.
} else {
	// Old default. Keep this code until a later behavior version removes it.
}
```

`Behavior(v)` is true when `BehaviorVersion >= v`.
Gate each new behavior with the number of the release that adds it.
Write a test for each side of the gate.

## The rule

A new behavior never changes an existing app without the opt-in.
These rules follow from it:

- Ship a changed default behind a new behavior version. Never change the default of the lower numbers.
- Read the number only through `config.Base.Behavior(v)`. Do not read the environment variable in other code.
- Keep the old path until the deprecation window ends. See `docs/policy/support.md`.
- A bug fix is not a behavior change. Fix a security flaw for every behavior version.
- List each new behavior version in the changelog, with the old and new behavior.
