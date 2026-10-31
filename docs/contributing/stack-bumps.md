# Bump the sidecar pins

The sidecar service templates pin their images, crates, Python packages, and Node packages.
The pins are in `internal/stackversions/stackversions.yaml`.
The lockfiles are in `internal/gen/service/templates/`: `Cargo.lock`, `uv.lock`, and `pnpm-lock.yaml`.
All of these are shared files. Only the main loop or CI changes them.

The script `scripts/bump-services.sh` helps with both tasks.
It needs `curl`, `jq`, and network access for `report`, and Docker and network access for `lock`.
A dry run needs none of these tools.

## Report newer versions

Run the `report` mode. It lists newer versions of the sidecar images, crates, Python packages, and Node packages.
It writes nothing.

<!-- doctest: skip -->
```console
$ scripts/bump-services.sh report
```

## Refresh the lockfiles

Run the `lock` mode after you change a pin or a manifest.
It regenerates `Cargo.lock`, `uv.lock`, and `pnpm-lock.yaml` inside the pinned images, from the pins of the manifests.
Use `--dry-run` to print the commands and change nothing.
`BUMP_SERVICES_DRY_RUN=1` has the same effect.
Set `BUMP_SERVICES_FRESH=1` to refresh the transitive dependencies from the current pins.

<!-- doctest: skip -->
```console
$ scripts/bump-services.sh lock --dry-run
```

After a lock refresh, run `make golden PACK=service-rust`, `make golden PACK=service-python`, and `make golden PACK=service-node`.
The golden trees must match the new lockfiles.
Run `make script RUN=app_gate_services` to build and test the three templates.

## The weekly canary

The workflow `.github/workflows/canary-services.yml` runs every week.
Its `report` job writes the output of `report` to the job summary.
Its `canary` job runs `lock` and then the script `app_gate_services`.
The workflow opens no pull request.
Read the summary, then change the pins by hand in one commit.

## Keep the pins in step

`TestTemplatePins` checks that the manifests match `stackversions.yaml` exactly.
The OpenTelemetry crates share one minor version.
Change them together.
A tool that opens bump pull requests for the whole stack is a later issue.
