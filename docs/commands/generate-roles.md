# Generate the role set

Run `spinup g roles` from the application root.
The application must include the `auth` module.
The command has no arguments and no flags of its own.

The command reads the `[auth]` table of `spinup.toml`.
It writes `api/internal/roles/roles.gen.go`.
The file has one constant for each role and the value `Set`.
`main.go` passes `Set` to `auth.WithRoles`.
See [Accounts and sessions](../auth.md#roles) for the table, the guard, and the `user` command.

```console
$ spinup new roleapp --module example.com/roleapp --modules ci,auth --no-git --non-interactive
...
$ cd roleapp
$ spinup g roles --dry-run
unchanged api/internal/roles/roles.gen.go (managed)
$ printf '\n[auth]\nroles = ["owner", "cook"]\nadmin_role = "owner"\ndefault_role = "cook"\n' >> spinup.toml
$ spinup g roles --dry-run
update api/internal/roles/roles.gen.go (managed)
$ spinup g roles
Created 0, updated 1 files
$ tail -n 2 api/internal/roles/roles.gen.go
// Set is the role set of this app. Pass it to auth.WithRoles.
var Set = auth.Roles{Names: []string{Owner, Cook}, Admin: Owner, Default: Cook}
$ spinup g roles
Created 0, updated 0 files
```

The file is `managed`. Do not edit it.
Run the command again after each change to `[auth]`.
If nothing changed, the command writes nothing.
The command has no post-step. It does not edit `spinup.toml`.

If you remove a role and run the command, the constant of the role goes away.
Generated handlers that use the constant then fail to compile.
This is wanted, because the compiler finds each use.

## Errors

If the app has no `auth` module, or `[auth]` breaks a rule, the command fails.
The error names the rule and the value:

```console
$ sed -i.bak 's/admin_role = "owner"/admin_role = "chef"/' spinup.toml
$ spinup g roles
Error: generator "roles" failed: auth admin_role "chef" must be in roles.
  Try: correct [auth] in spinup.toml and run spinup g roles.
[exit 1]
$ mv spinup.toml.bak spinup.toml
```

```console
$ spinup new noauth --module example.com/noauth --api-only --no-git --non-interactive
...
$ cd noauth
$ spinup g roles
Error: generator "roles" failed: cannot generate roles: the app has no auth module
  Try: spinup add auth
[exit 1]
```

## The doctor check

The check `roles-drift` runs in `spinup doctor`.
It warns in these cases:

- `roles.gen.go` differs from `[auth]`, or the file is missing. The fix is `spinup g roles`.
- `api/cmd/api/main.go` has no `auth.WithRoles(` call. Add `auth.WithRoles(roles.Set)`, `auth.WithRegistration(...)`, and `app.WithCommand("user", auth.UserCommand(a))` to `main.go`.

The check skips an app without the `auth` module.
