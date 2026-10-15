# releases

Run `spinup releases list --env staging` to inspect the release history.
The list includes the ID, commit, time, user, migration version, and current marker.
The command `spinup releases --env staging` also lists the history.
Use `--json` for structured output.

```console
$ spinup releases --help
...
$ spinup releases list --help
...
$ spinup releases show --help
...
$ spinup releases prune --keep 0 --env staging
...
  Try: use releases list, show <release-id>, or prune --keep <positive number>
[exit 1]
```

Run `spinup releases show <id> --env staging` to inspect a snapshot.
The snapshot contains secret hashes only.
The command refuses a snapshot with an invalid hash.

Run `spinup releases prune --keep 5 --env staging` to remove old release directories.
The command takes the deploy lock.
It always retains the current release and the previous release.
Thus, it can retain more releases than `--keep` specifies.
It retains Docker images for subsequent maintenance.
Protected environments require confirmation.
