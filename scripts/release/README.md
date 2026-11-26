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

## License

spinup is under the MIT license. Copyright (c) 2026 Taylor King and the spinup authors.
Read the full text in `LICENSE`.

The files that spinup writes into your app are yours. You can use, change, and distribute them under any terms, and you do not need to keep the spinup copyright notice.
This permission covers the output of the template packs and the generators.
It does not cover third-party files that spinup copies into an app. `THIRD_PARTY_NOTICES.md` lists them.
It does not cover the kit. Your app imports the kit as a Go module under the MIT license.
