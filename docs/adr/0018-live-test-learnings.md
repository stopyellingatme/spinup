# ADR 0018: Changes from the first live DigitalOcean test

Status: accepted (2026-10-23)

## Context

On 2026-10-23 we deployed the todo app to a real DigitalOcean droplet with `spinup deploy digital-ocean` (ADR 0017).
The test passed, but it showed these problems:

1. **Unpublished kit.** The kit was not published, because the repository had no remote.
   A generated API requires `github.com/stopyellingatme/spinup/kit v0.1.0`, and no such version existed.
   `spinup build --image` refuses a local kit replace, because the replace directory is outside the Docker build context.
   We had to build the images by hand.
2. **Upgrade without the variable.** `spinup upgrade` in an app with a local kit replace failed in `go mod tidy`.
   It failed because `SPINUP_KIT_REPLACE` was not set, although the replace was in `api/go.mod`.
3. **No flag for the typed confirmation.** For a protected environment, only a person in a terminal can type the environment name.
   The other way is `--yes` together with `CI=true`.
   This breaks the rule "every prompt has a flag".
4. **Gaps in the fake API.** The real API has two rules that `dofake` did not model.
   A firewall refuses a tag that does not exist.
   Destroy also left two empty tags in the account.
   Both are fixed (`5d165fc`, `885dc5f`), but there can be more such gaps.
5. **Small defects:**
   - `status --env` shows the team UUID, but `status` with no environment shows the team name.
   - The SSH cache writes one `known_hosts-<hash>` file for each set of pinned keys, and it never removes old files.
   - `codemod.SetTOML` appends a new table with no blank line before it.
6. **Manual live test.** The live test was a manual sequence. Nothing repeats it.

## Decisions

### 1. Publish the repository and pin the kit by tag

- The repository is public at `github.com/stopyellingatme/spinup`.
- The maintainers push a kit pre-release tag `kit/v0.1.0-alpha.<n>`, where `<n>` is the commit count (as `make install` uses).
  The release workflow (`docs/contributing/release.md`) still pushes `kit/v<version>` for each CLI release.
- `stackversions.yaml` pins the kit version in `go_modules`. The key is `github.com/stopyellingatme/spinup/kit`.
  The `api` pack renders the require line with `[[ gomod "github.com/stopyellingatme/spinup/kit" ]]`.
  The pack `go.sum` holds the hash lines of that version.
- `scripts/bump-stack.sh` reads the pin from `stackversions.yaml`. It no longer has its own default.
- The release check fails when the major or minor version of the kit pin differs from the major or minor version of the CLI candidate.
- A push of the tag, and a change of the pin, are main loop steps.

### 2. Put a local kit replace in the image build context

- When `api/go.mod` replaces the kit with a local directory, `spinup build --image` passes that directory to BuildKit as the named build context `kit`.
  The deploy build does the same.
- `deploy/Dockerfile.api` has an empty stage `FROM scratch AS kit` and copies it to `/kit`.
  When `/kit/go.mod` exists, the build runs `go mod edit -replace` with the module path and `/kit` before `go mod download`.
  Without the build context, the stage is empty and the build uses the required version.
- A replace to a missing directory, or a replace of another module to a local path, still fails with a `Try:` line.

### 3. Keep a local kit replace on upgrade

`spinup upgrade` and `spinup module add` keep a local kit replace when these two conditions are true:
- `SPINUP_KIT_REPLACE` is not set.
- The current `api/go.mod` replaces the kit with a directory that exists.

`SPINUP_KIT_REPLACE` still wins when it is set.

### 4. A flag for the typed confirmation

- The global flag `--confirm-env <name>` answers the typed confirmation of a protected environment without a terminal.
- The value must equal the environment name, so the flag works as a typed answer.
- `--yes` with `CI=true` still works.
- `project.Confirm` gets a field `Name`. All callers build the confirmation through one helper in `internal/cli`.

### 5. Close the gaps in the fake API

We compare `dofake` with the DigitalOcean API reference for each endpoint that spinup calls.
`dofake` models each rule that can change the result of a spinup call.
Each new rule has a test.

### 6. Small fixes

- `status` shows the team name and the UUID in both forms.
- The SSH cache keeps one pin file for each host and port.
  The file name does not depend on the keys, and spinup replaces the file atomically when the pins change.
- `codemod.SetTOML` writes a blank line before a new table.

### 7. A repeatable live test

`scripts/do-live-test.sh` repeats the live test sequence on a real account:
1. Provision with a `kill -9` during the droplet wait, then run again.
2. Run again, and check that nothing changes.
3. Force a token refresh.
4. Run a plain deploy with no DigitalOcean token.
5. Destroy, and check that only the SSH key remains.

The script costs money.
It runs only when `SPINUP_DO_LIVE=1` is set, and it asks before the create and before the destroy.
It is not part of `make` or CI.
