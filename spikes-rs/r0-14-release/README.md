# R0.14 release probe

Read the result in `../../docs/design/rust-spike/r0-14-release.md`.
The six-target release remains blocked on Windows arm64.

Use the lane environment before these commands.
Keep `CARGO_BUILD_JOBS=4` and a separate `CARGO_TARGET_DIR`.
Run these commands from this directory:

```sh
python3 probe.py full goreleaser release --snapshot --clean --skip=publish,sign --parallelism=1
python3 probe.py matrix
python3 probe.py installed
python3 verify.py
python3 verify.py --require-six
```

The full command and the matrix return failure for the missing Windows arm64 target.
The installed subset produces five archives and one `checksums.txt`.
The acceptance check requires all six archives and a release license.
It currently fails.

The Rust builder uses `cargo zigbuild`.
The post-build hook uses `cargo auditable zigbuild` before packaging.
The SBOM step reads the embedded metadata with syft.
The snapshot command must include `--skip=sign`.
Snapshot mode alone does not skip cosign.

The probe records elapsed seconds and `uptime` before and after each command.
It retains logs and generated configurations in `evidence/`.
It stores archives in `dist/`.
Both directories remain untracked.
The committed `measurements.json` retains the observed results.

GoReleaser 2.18.3 copies binaries from `./target/`.
The probe links that path to `CARGO_TARGET_DIR` when necessary.
The `tools/rustup` guard only verifies installed targets.
It never invokes rustup or installs a component.

The installed GoReleaser edition rejects `builder: prebuilt`.
Use a supported release edition or a separate packaging stage for native artifacts.
No workflow file belongs to this spike.
