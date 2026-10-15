# Plugin trust policy

Tier-1 plugins contain declarative packs and generators.
The CLI never executes their code or installs their dependencies.
Review their generated files before application or execution.
Generated source can change the behavior of your app.

Commit the declaration, lock, `.spinup/plugins.allow`, and `.spinup/plugins.history` together.
Each allowlist line contains `<name> <source> <sha256>`.
Approval changes the declaration, allowlist, and history.
Approval leaves the lock unchanged.
Upgrade records the plugins rendered into the app.
A hash change requires another explicit approval with `--trust` or a terminal confirmation.
Ignore `.spinup/*` and permit `.spinup/plugins.allow` and `.spinup/plugins.history` in `.gitignore`.

The tree hash covers sorted portable paths, normalized contents, and file modes.
Text uses the normalization from `internal/manifest`.
Executable files use mode `0755`.
Other regular files use mode `0644`.
Each hash field includes its length to prevent ambiguous concatenation.

The CLI excludes the root `.git` directory.
It rejects symlinks, submodule declarations, special files, and unsafe paths.
Each tree has a limit of 5 MiB and 1000 entries, including directories.

Plugin outputs and `renamed_from` paths cannot target `.git`, `.spinup`, or `.github` at the project root.
They cannot target `spinup.toml` or `spinup.lock` at the project root.
They cannot contain a `.git` segment at any depth.
These comparisons ignore case.
The same restrictions apply to generator outputs.
After rendering each output path with the actual arguments, call `pack.CheckOutputPath`.

Git fetches use a shallow clone, a fixed version, disabled recursive submodules, and a two-minute timeout.
They request `blob:limit=5m` filtering.
The CLI checks the temporary directory every 10 milliseconds during Git commands.
It cancels a command above 20 MiB or 4256 entries, including Git metadata.
Writes can exceed these limits between checks.
The CLI removes the temporary directory after success or failure.

The CLI disables local file transport, external git protocols, global git configuration, and template hooks.
It does not prompt for git credentials.
Use an existing SSH agent for private repositories.

Offline resolution requires the cache.
The CLI checks NEW bytes against the allowlist.
The CLI checks OLD bytes against the lock and history.
Cache receipts bind an approved source to its hash.
Cache paths include the name, version, source hash, and tree hash.
Approving different bytes preserves the previous cache entry.

The project history records each approved name, version, source, and tree hash.
Historical version overrides require a matching project history record and cached tree.
The lock identifies the previously rendered version and hash.
A missing history record stops resolution, even when another project cached that version.
If the lock has no matching identity, multiple hashes for one historical version stop resolution.
Use distinct plugin versions when the contents change.

A missing historical cache entry stops the upgrade.
Keep historical cache entries when you need reproducible upgrades.
Old cache directories without hash keys cannot authorize resolution.
Approve the current source again to populate the new cache and project history.

The `spinup` range supports exact versions, comparator conjunctions, `||`, `^`, `~`, and `*`.
Development builds validate the range syntax and accept valid ranges.
Released builds must satisfy the range.

Tier-2 executable plugins are outside this milestone.
Tier-2 plugins run with the full privileges of the user.
They need a committed allowlist and explicit trust.
