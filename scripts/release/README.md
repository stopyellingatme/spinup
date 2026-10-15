# spinup

spinup generates and operates applications with Go, SvelteKit, PostgreSQL, and Docker Compose.
It tracks generated files with `spinup.lock` and supports upgrades with a three-way merge.

Run `spinup --help` to inspect the commands.
Run `spinup version` to inspect the binary version.
Read the guides at <https://github.com/stopyellingatme/spinup/tree/main/docs>.

Verify the archive checksum against `checksums.txt` before installation.
When cosign is available, verify `checksums.txt.sigstore.json` against the release workflow.
The workflow identity is under `stopyellingatme/spinup` on GitHub.

The release also includes the template archive, its ed25519 signature, compatibility metadata, and the stack versions.
