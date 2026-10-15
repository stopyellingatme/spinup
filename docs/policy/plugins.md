# Plugin trust policy

Tier-1 plugins contain declarative packs and generators.
The CLI never executes their code or installs their dependencies.
Review their generated files before application or execution.
Generated source can change the behavior of your app.

Commit the declaration, lock, and `.spinup/plugins.allow` together.
Each allowlist line contains `<name> <source> <sha256>`.
The CLI requires matching records before resolution.
A hash change requires another explicit approval with `--trust` or a terminal confirmation.

The tree hash covers sorted portable paths, normalized contents, and file modes.
Text uses the normalization from `internal/manifest`.
Executable files use mode `0755`.
Other regular files use mode `0644`.
Each hash field includes its length to prevent ambiguous concatenation.
The CLI excludes the root `.git` directory.
It rejects symlinks, submodule declarations, special files, and unsafe paths.
Each tree has a limit of 5 MiB and 1000 entries, including directories.

Git fetches use a shallow clone, a fixed version, disabled recursive submodules, and a two-minute timeout.
The CLI disables local file transport, external git protocols, global git configuration, and template hooks.
It does not prompt for git credentials.
Use an existing SSH agent for private repositories.

Offline resolution requires the cache.
The CLI checks cached bytes against the lock and allowlist.
Cache receipts bind an approved source to its hash.
Historical version overrides require a retained receipt from an earlier approval.
They also require current project authorization for that plugin and source.
A missing historical cache entry stops the upgrade.
Keep historical cache entries when you need reproducible upgrades.

The `spinup` range supports exact versions, comparator conjunctions, `||`, `^`, `~`, and `*`.
Development builds validate the range syntax and accept valid ranges.
Released builds must satisfy the range.

Tier-2 executable plugins are outside this milestone.
Tier-2 plugins run with the full privileges of the user.
They need a committed allowlist and explicit trust.
