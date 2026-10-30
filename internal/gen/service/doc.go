// Package service is the generator `spinup generate service <name> --lang rust|python`
// (ADR 0022 §2). It writes services/<name>/ and appends a [[services]] table to spinup.toml.
//
// The lockfiles in templates/ are shared files from the services pre-seed (main loop only):
// templates/rust/Cargo.toml, templates/rust/Cargo.lock, templates/python/pyproject.toml, and
// templates/python/uv.lock. Their versions equal the crates and python_packages pins in
// stackversions.yaml.
package service
