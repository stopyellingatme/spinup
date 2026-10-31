// Package service is the generator `spinup generate service <name> --lang rust|python|node`
// (ADR 0022 §2). It writes services/<name>/ and appends a [[services]] table to spinup.toml.
//
// The lockfiles in templates/ are shared files from the services pre-seed (main loop only):
// templates/rust/Cargo.toml, templates/rust/Cargo.lock, templates/python/pyproject.toml,
// templates/python/uv.lock, templates/node/package.json, and templates/node/pnpm-lock.yaml.
// Their versions equal the crates, python_packages, and node_packages pins in stackversions.yaml.
package service
