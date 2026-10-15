# Manage pack plugins

A pack plugin contains templates and metadata.
The CLI does not execute plugin code.

```console
$ spinup plugin --help
...
$ spinup new demo --api-only --module example.com/demo --modules '' --no-git --non-interactive
...
$ cd demo
$ spinup plugin list --json
[]
$ spinup plugin verify
$ spinup plugin add git:https://example.invalid/plugin --trust
...
[exit 1]
```

Run `spinup plugin add path:./plugins/example --trust` to trust a local plugin.
For a git source, supply a fixed tag with `--version v1.0.0`.
Supported URLs use `https`, `ssh`, or `git`.
The CLI rejects local git transport.
Use a `path:` source for a local directory.

Before approval, the command prints the name, version, source, hash, files, and generators.
A terminal asks for `yes`.
Without a terminal, the command requires `--trust`.
The global `--yes` flag does not grant plugin trust.

The command changes `spinup.toml`, `spinup.lock`, and `.spinup/plugins.allow`.
It preserves unrelated configuration and comments.
It does not render the pack.
Run `spinup upgrade` to review the generated files.

Run `spinup plugin list --json` to read the declarations and locked hashes.
Run `spinup plugin info example` to inspect verified metadata, generators, and file paths.
Run `spinup plugin verify` in CI to fetch sources again and check their hashes.
Verification exits with code 1 when a source differs from its trust records.

Run `spinup plugin update example --version v2.0.0 --trust` to approve another version.
For a changed local source, omit `--version` to approve the same declared version.
The command prints the old and new hashes.
Run `spinup upgrade --plan` to review the resulting upgrade.

Run `spinup plugin remove example` to remove the declaration, lock entry, and allowlist line.
The command lists previously rendered files from the lock.
It retains those files and their file entries.

Use `--dry-run` to inspect a proposed change without writing trust records or the cache.
Use `--offline` to require a previously approved cache entry.
The cache resides under the user cache directory, at `spinup/plugins/<name>/<version>/`.
An absent offline entry produces an error.
