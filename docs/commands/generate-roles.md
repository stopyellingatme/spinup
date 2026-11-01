---
title: generate roles — define who can do what
parent: Command guides
---

# generate roles — define who can do what

Roles tell your app which signed-in users may use which parts of it.
You list the roles in the `[auth]` table of `spinup.toml`.
`spinup g roles` turns that list into Go constants, so your handlers never repeat a role name as a string.

The app needs the `auth` module.
The command has no arguments and no flags of its own.

```console
$ spinup new roleapp --module example.com/roleapp --modules ci,auth --no-git --non-interactive
...
$ cd roleapp
$ printf '\n[auth]\nroles = ["owner", "cook"]\nadmin_role = "owner"\ndefault_role = "cook"\n' >> spinup.toml
$ spinup g roles
Created 0, updated 1 files
$ tail -n 2 api/internal/roles/roles.gen.go
// Set is the role set of this app. Pass it to auth.WithRoles.
var Set = auth.Roles{Names: []string{Owner, Cook}, Admin: Owner, Default: Cook}
```

The file `api/internal/roles/roles.gen.go` has one constant for each role, and the value `Set`.
`main.go` passes `Set` to `auth.WithRoles`, so the app knows the roles at run time.

## The [auth] keys

| Key | What it sets | Default | Example |
| --- | --- | --- | --- |
| `roles` | The role names, 1 to 16 of them. | `["manager", "staff"]` | `roles = ["owner", "cook"]` |
| `admin_role` | The role of administrators. It must be in `roles`. | `"manager"` | `admin_role = "owner"` |
| `default_role` | The role of a new account. It must be in `roles`. | `"staff"` | `default_role = "cook"` |

A role name starts with a lower-case letter and has up to 32 lower-case letters, digits, and underscores.
It cannot have a double underscore or end with an underscore, and each name must be unique.
`front_desk` becomes the constant `FrontDesk`.
If you omit the `[auth]` table, the defaults apply and the app has the roles `manager` and `staff`.

## Use a role in your code

Use the constants in handlers:

```go
if !user.HasRole(roles.Owner) {
	// answer 403
}
```

The generators write this check for you.
`spinup g resource --role owner` and `spinup g page --server --role owner` guard a whole resource or page.
See [Roles](../build/roles.md) for the guard, the registration modes, and the `user` command.

## Change the roles

Edit `[auth]`, then run the command again.
The file is managed, so do not edit it by hand.
If nothing changed, the command writes nothing.

```console
$ spinup g roles
Created 0, updated 0 files
```

If you remove a role, its constant goes away.
Every handler that used the constant then fails to compile.
This is on purpose: the compiler finds each place that you must fix.

## Troubleshooting

If `[auth]` breaks a rule, the command names the rule and the value:

```console
$ spinup new badapp --module example.com/badapp --modules ci,auth --no-git --non-interactive
...
$ cd badapp
$ printf '\n[auth]\nroles = ["owner", "cook"]\nadmin_role = "chef"\ndefault_role = "cook"\n' >> spinup.toml
$ spinup g roles
Error: generator "roles" failed: auth admin_role "chef" must be in roles.
  Try: correct [auth] in spinup.toml and run spinup g roles.
[exit 1]
```

If the app has no `auth` module, the command fails.
Run `spinup module add auth` to add it.

## The roles-drift check

`spinup doctor` runs the check `roles-drift` in an app with the `auth` module.
It warns in two cases:

| Warning | Fix |
| --- | --- |
| `roles.gen.go` differs from `[auth]`, or it is missing. | Run `spinup g roles`. |
| `api/cmd/api/main.go` does not call `auth.WithRoles(`. | Add the three lines below to `main.go`. |

An app made before roles existed needs these lines in `main.go`:

```go
auth.WithRoles(roles.Set),
auth.WithRegistration(auth.RegistrationMode(os.Getenv("AUTH_REGISTRATION"))),
// and, with the other commands:
app.WithCommand("user", auth.UserCommand(authn)),
```

<!-- doctest: skip -->
```console
$ spinup doctor
...
warn	roles-drift	spinup.toml has invalid roles
Try: correct [auth] in spinup.toml and run spinup g roles
```

## See also

- [Roles](../build/roles.md): the guard, registration modes, and the `user` command
- [generate resource](generate-resource.md) and [generate page](generate-page.md): the `--role` flag
- [doctor](doctor.md)
- Flag reference: [`generate roles`](../site/commands/generate-roles.md)
