---
title: Bump the sidecar pins
parent: Contributing
nav_order: 5
---

# Bump the sidecar pins

The sidecar service templates pin their images, crates, Python packages, and Node packages.
The pins are in `internal/stackversions/stackversions.yaml`.
The lockfiles are in `internal/gen/service/templates/`: `rust/Cargo.lock`, `python/uv.lock`, `python-pdf/uv.lock`, and `node/pnpm-lock.yaml`.
Change them in one commit, with a person who reviews the result.

The script `scripts/bump-services.sh` helps with two tasks: `report` lists newer versions, and `lock` refreshes the lockfiles.
It needs `curl`, `jq`, and network access for `report`, and Docker and network access for `lock`.
A dry run needs none of these tools.

## Report newer versions

Run the `report` mode. It lists newer versions of the crates, Python packages, and Node packages. For each image, it prints the pinned tag and `manual check`.
It writes nothing.

<!-- doctest: skip -->
```console
$ scripts/bump-services.sh report
...
```

## Refresh the lockfiles

Run the `lock` mode after you change a pin or a manifest.
It regenerates the four lockfiles inside the pinned images, from the pins of the manifests.
Use `--dry-run` to print the commands and change nothing.
`BUMP_SERVICES_DRY_RUN=1` has the same effect.
Set `BUMP_SERVICES_FRESH=1` to refresh the transitive dependencies from the current pins.

<!-- doctest: skip -->
```console
$ scripts/bump-services.sh lock --dry-run
```

After a lock refresh, run `make golden PACK=service-rust`, `make golden PACK=service-python`, and `make golden PACK=service-node`.
The golden trees must match the new lockfiles.
Run `SPINUP_APP_GATE=1 make script RUN=app_gate_services` to build and test the three templates.
Without `SPINUP_APP_GATE=1`, `make script` skips every `app_gate_*` script.
If you changed the PDF template, also run `SPINUP_APP_GATE=1 make script RUN=app_gate_pdf`.

## The weekly canary

The workflow `.github/workflows/canary-services.yml` runs every week.
Its `report` job writes the output of `report` to the job summary.
Its `canary` job runs `lock` and then the script `app_gate_services`.
The direct pins and the image digests do not change, so the canary tests newer transitive dependencies only.
The workflow opens no pull request.
Read the summary, then change the pins by hand in one commit.

## Keep the pins in step

`TestTemplatePins` checks that the manifests match `stackversions.yaml` exactly.
The OpenTelemetry crates share one minor version.
Change them together.
Nothing opens bump pull requests for the whole stack yet.
The weekly workflows `bump.yml` and `canary-pins.yml` only report. Change the pins by hand.

## A normal change

1. Edit the pin in `internal/stackversions/stackversions.yaml` and in the manifest of the template.
2. Run `scripts/bump-services.sh lock` to refresh the lockfiles.
3. Run `make golden PACK=service-rust`, `make golden PACK=service-python`, and `make golden PACK=service-node`.
4. Run `SPINUP_APP_GATE=1 make script RUN=app_gate_services`.
5. Commit the pins, the lockfiles, and the golden trees together.

## See also

- [Release spinup](release.md)
- [Upgrade matrix](upgrade-matrix.md)
- [Sidecar services](../services.md)
- [PDF reports](../pdf-reports.md)
