#!/usr/bin/env python3
"""Check real archive contents, SBOM dependencies, and SHA-256 checksums."""
import hashlib
import json
from pathlib import Path
import re
import sys
import tarfile
import zipfile

ROOT = Path(__file__).resolve().parent
VERSION = "0.0.0-r0.14"
EXPECTED = {f"spinup_{VERSION}_{os}_{arch}.{extension}"
            for os, extension in [("linux", "tar.gz"), ("darwin", "tar.gz"), ("windows", "zip")]
            for arch in ["amd64", "arm64"]}


def verify(require_six=False):
    archives = sorted(p for p in (ROOT / "dist" / "installed").rglob("spinup_*")
                      if p.name.endswith((".tar.gz", ".zip")))
    found = set()
    results = []
    for archive in archives:
        assert archive.name in EXPECTED, archive
        assert archive.name not in found, f"duplicate archive: {archive.name}"
        found.add(archive.name)
        if archive.suffix == ".zip":
            with zipfile.ZipFile(archive) as content:
                names = set(content.namelist())
        else:
            with tarfile.open(archive) as content:
                names = set(content.getnames())
        binary = "spinup.exe" if archive.suffix == ".zip" else "spinup"
        assert {binary, "README.md", "CHANGELOG.md"} <= names, (archive, names)
        sbom_path = archive.parent / re.sub(r"\.(tar\.gz|zip)$", ".sbom.json", archive.name)
        sbom = json.loads(sbom_path.read_text())
        packages = {p["name"] for p in sbom["packages"]}
        assert {"cli-skel", "ring", "rustls", "ureq", "zstd", "zstd-sys"} <= packages, packages
        assert not {"openssl", "openssl-sys", "aws-lc-rs", "aws-lc-sys"} & packages, packages
        checksums = archive.parent / "checksums.txt"
        hashes = {}
        for line in checksums.read_text().splitlines():
            digest, name = line.split(maxsplit=1)
            path = archive.parent / name.lstrip("*")
            assert hashlib.sha256(path.read_bytes()).hexdigest() == digest, path
            hashes[path.name] = digest
        assert archive.name in hashes
        assert sbom_path.name in hashes
        results.append(dict(archive=archive.name, archive_bytes=archive.stat().st_size,
                            contents=sorted(names), license_present=any("LICENSE" in n for n in names),
                            sbom_packages=len(packages), checksums_verified=len(hashes)))
    summary = dict(archives=len(found), missing=sorted(EXPECTED - found), results=results)
    print(json.dumps(summary, indent=2))
    if require_six:
        assert found == EXPECTED, summary["missing"]
        assert all(r["license_present"] for r in results), "The repository has no release LICENSE file."
    assert found, "No release archives exist."
    return summary


if __name__ == "__main__":
    verify(require_six="--require-six" in sys.argv[1:])
