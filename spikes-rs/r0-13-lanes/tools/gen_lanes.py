#!/usr/bin/env python3
"""Spike r0-13: writes the scaled workspaces for the lane and gate load test.

Usage:
  gen_lanes.py cli  --out DIR [--lines N]   the CLI workspace (45 CLI crates
                                            from rust-plan.md section 1, plus
                                            spinup-kit, spinup-kit-testkit,
                                            and spinup-plugin-sdk)
  gen_lanes.py gate --out DIR               the gate tree: one shared kit, 8
                                            generated apps, and the reference
                                            app for rs-ref-check

The CLI crates use the R0.12 cli-skel dependencies. Each crate has generated
modules (serde records, errors, and one function per dependency flavor),
unit tests in each module, and one integration test binary
`tests/it/main.rs`. The bin crate `spinup` is the R0.12 cli-skel (the Go
command tree as clap stubs, three embedded archives). `spinup-kit` is the
R0.12 full kit. `--lines` sets the total generated lines of the 45 CLI
crates (src plus tests).

The gate apps derive from the R0.11 app (the R0.12 full kit plus 20
resources from the R0.10 generator): api, auth, jobs, mail (feature sets of
the todo app), three gen-resource trees (2, 5, and 10 resources), and the
corpus app (20 resources). All apps share one kit path. The `.sqlx/` data of
R0.11 covers every query, so no app needs a database to build.

Both commands run `cargo update --offline --workspace` to write each
Cargo.lock from the R0.12 or R0.11 lock (no new registry versions).
"""

import argparse
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

SPIKE = Path(__file__).resolve().parent.parent
SPIKES = SPIKE.parent
R11 = SPIKES / "r0-11-devloop"
REPO = SPIKES.parent

# (crate, workspace deps, weight). The crate table of rust-plan.md section 1.
CRATES = [
    ("ui", [], 1),
    ("compat", [], 0.5),
    ("vfs", [], 1),
    ("manifest", [], 1),
    ("stack", [], 0.5),
    ("editors", [], 1.5),
    ("exec", [], 0.5),
    ("project", ["ui", "manifest", "exec"], 1),
    ("packs", ["vfs", "manifest", "stack"], 1.5),
    ("tplstore", ["packs", "manifest", "compat", "stack", "project"], 1),
    ("render", ["packs", "stack", "manifest", "vfs", "project", "ui"], 1.5),
    ("merge", ["exec", "compat"], 1.5),
    ("doctor", ["exec", "manifest", "project", "ui", "editors"], 1),
    ("plugin-api", ["manifest", "vfs"], 0.5),
    ("migrate", ["exec", "manifest", "stack", "packs", "editors", "project"], 1),
    ("gen", ["vfs", "manifest", "exec", "ui", "project"], 1),
    ("upgrade", ["merge", "migrate", "render", "tplstore", "plugin-api", "doctor", "project", "manifest", "vfs", "exec", "compat", "editors", "ui"], 2),
    ("drift", ["upgrade", "doctor", "tplstore", "vfs"], 0.5),
    ("newapp", ["render", "packs", "tplstore", "stack", "manifest", "vfs", "exec", "ui", "plugin-api"], 1),
    ("db", ["exec", "project", "ui"], 1),
    ("build", ["exec", "manifest", "project", "stack", "ui"], 0.5),
    ("appcmd", ["exec", "manifest", "project", "stack", "gen", "doctor", "ui"], 1),
    ("gen-web", ["gen", "vfs", "manifest", "project", "editors"], 1.5),
    ("gen-app", ["gen", "gen-web", "db", "render", "vfs", "manifest", "project"], 2),
    ("remote", ["exec", "project"], 1),
    ("secrets", ["exec", "project", "editors"], 1),
    ("server", ["exec", "project", "remote", "doctor", "ui"], 1),
    ("deploy", ["remote", "exec", "secrets", "project", "manifest", "drift", "db", "build", "ui"], 2),
    ("cloud", [], 1.5),
    ("dofake", [], 1),
    ("provision", ["cloud", "deploy", "secrets", "remote", "server", "editors", "project"], 1),
    ("infra", ["exec", "project", "editors", "stack"], 1),
    ("backup", ["deploy", "remote", "render", "packs", "project"], 1.5),
    ("maintain", ["backup", "deploy", "remote", "stack", "project"], 1),
    ("monitor", ["remote", "deploy", "project"], 1),
    ("doctor-checks", ["doctor", "backup", "remote", "gen", "merge", "compat"], 1.5),
    ("plugin", ["plugin-api", "plugin-sdk", "gen", "packs", "render", "doctor"], 1.5),
    ("selfupdate", ["compat", "exec"], 0.5),
    ("ci", ["gen", "render", "packs", "doctor"], 1),
    ("release", ["tplstore", "stack", "compat"], 1),
    ("cli", None, 2),  # None: every CLI crate above
    ("testutil", ["render", "packs", "vfs"], 0.5),
    ("testscript", [], 1),
    ("doctest", [], 0.5),
]
# The bin crate `spinup` (R0.12 cli-skel, depends on cli) is the 45th CLI crate.
EXTRA = [("plugin-sdk", [], 0.5), ("kit-testkit", [], 0.5)]

# Registry dependencies: the cli-skel set (R0.12), with its versions.
REG = {
    "anyhow": '"1.0.104"',
    "clap": '{ version = "4.6.7", features = ["derive", "env", "string", "wrap_help"] }',
    "ed25519-dalek": '"3.0.0"',
    "minijinja": '{ version = "3.0.3", features = ["builtins", "json", "loop_controls", "macros", "serde", "urlencode"] }',
    "regex": '"1.13.1"',
    "rustls": '{ version = "0.23.46", default-features = false, features = ["ring", "std", "tls12"] }',
    "semver": '"1.0.28"',
    "serde": '{ version = "1.0.229", features = ["derive"] }',
    "serde_json": '"1.0.154"',
    "sha2": '"0.11.1"',
    "tar": '"0.4.46"',
    "thiserror": '"2.0.21"',
    "toml": '"1.1.8"',
    "toml_edit": '"0.25.17"',
    "ureq": '{ version = "3.5.0", default-features = false, features = ["rustls", "json"] }',
    "walkdir": '"2.5.0"',
    "zstd": '"0.14.3"',
}

FLAVORS = ["toml", "regex", "minijinja", "sha2", "semver", "toml_edit", "walkdir", "zstd", "tar", "ed25519", "ureq"]
FLAVOR_DEPS = {
    "toml": ["toml"],
    "regex": ["regex"],
    "minijinja": ["minijinja"],
    "sha2": ["sha2"],
    "semver": ["semver"],
    "toml_edit": ["toml_edit"],
    "walkdir": ["walkdir"],
    "zstd": ["zstd"],
    "tar": ["tar"],
    "ed25519": ["ed25519-dalek"],
    "ureq": ["ureq", "rustls"],
}

LINES_PER_MODULE = 150  # measured: one module plus its it file, after rustfmt


def crate_name(short):
    return f"spinup-{short}"


def ident(short):
    return "spinup_" + short.replace("-", "_")


def flavor_fn(flavor, n):
    """One function (and its unit test body) that uses a dependency."""
    e = f"Error{n}"
    i = f"Item{n}"
    if flavor == "toml":
        return (
            f"""/// Parses a record from TOML.
pub fn from_toml(s: &str) -> Result<{i}, {e}> {{
    toml::from_str(s).map_err(|err| {e}::Parse("toml".to_owned(), err.to_string()))
}}

/// Writes a record as TOML.
pub fn to_toml(item: &{i}) -> Result<String, {e}> {{
    toml::to_string(item).map_err(|err| {e}::Parse("toml".to_owned(), err.to_string()))
}}
""",
            f"""        let it = {i}::new("alpha");
        let back = from_toml(&to_toml(&it).unwrap()).unwrap();
        assert_eq!(it, back);""",
        )
    if flavor == "regex":
        return (
            f"""static NAME_{n}: LazyLock<regex::Regex> =
    LazyLock::new(|| regex::Regex::new(r"^[a-z][a-z0-9-]{{0,62}}$").expect("a valid pattern"));

/// Reports whether `s` is a valid record name.
pub fn valid_name(s: &str) -> bool {{
    NAME_{n}.is_match(s)
}}

/// Returns the words of `s` that are valid names.
pub fn names(s: &str) -> Vec<&str> {{
    s.split_whitespace().filter(|w| valid_name(w)).collect()
}}
""",
            """        assert!(valid_name("my-app"));
        assert!(!valid_name("My App"));
        assert_eq!(names("ok NO fine"), vec!["ok", "fine"]);""",
        )
    if flavor == "minijinja":
        return (
            f"""/// Renders the record with a minijinja template.
pub fn render(item: &{i}) -> Result<String, {e}> {{
    let mut env = minijinja::Environment::new();
    env.add_template(
        "item",
        "{{{{ name }}}}@{{{{ version }}}}{{% for t in tags %}} #{{{{ t }}}}{{% endfor %}}",
    )
    .map_err(|err| {e}::Parse("template".to_owned(), err.to_string()))?;
    env.get_template("item")
        .and_then(|t| t.render(minijinja::Value::from(minijinja::value::Serde(item))))
        .map_err(|err| {e}::Parse("render".to_owned(), err.to_string()))
}}
""",
            f"""        let mut it = {i}::new("alpha");
        it.tags.push("x".to_owned());
        assert_eq!(render(&it).unwrap(), "alpha@0.1.0 #x");""",
        )
    if flavor == "sha2":
        return (
            f"""/// Returns the sha256 of the JSON form, in hex.
pub fn digest(item: &{i}) -> String {{
    use sha2::Digest;
    sha2::Sha256::digest(item.to_json().as_bytes())
        .iter()
        .map(|b| format!("{{b:02x}}"))
        .collect()
}}
""",
            f"""        let a = digest(&{i}::new("alpha"));
        assert_eq!(a.len(), 64);
        assert_ne!(a, digest(&{i}::new("beta")));""",
        )
    if flavor == "semver":
        return (
            f"""/// Reports whether the version of `a` is newer than the version of `b`.
pub fn newer(a: &{i}, b: &{i}) -> Result<bool, {e}> {{
    let parse = |v: &str| {{
        semver::Version::parse(v).map_err(|err| {e}::Parse("semver".to_owned(), err.to_string()))
    }};
    Ok(parse(&a.version)? > parse(&b.version)?)
}}
""",
            f"""        let a = {i}::new("a");
        let mut b = {i}::new("b");
        b.version = "0.2.0".to_owned();
        assert!(newer(&b, &a).unwrap());
        assert!(!newer(&a, &b).unwrap());""",
        )
    if flavor == "toml_edit":
        return (
            f"""/// Sets the key `name` in a TOML document and keeps its comments.
pub fn set_name(doc: &str, item: &{i}) -> Result<String, {e}> {{
    let mut d: toml_edit::DocumentMut = doc
        .parse()
        .map_err(|err: toml_edit::TomlError| {e}::Parse("toml".to_owned(), err.to_string()))?;
    d["name"] = toml_edit::value(item.name.as_str());
    Ok(d.to_string())
}}
""",
            f"""        let out = set_name("# keep\\nname = \\"old\\"\\n", &{i}::new("new")).unwrap();
        assert_eq!(out, "# keep\\nname = \\"new\\"\\n");""",
        )
    if flavor == "walkdir":
        return (
            f"""/// Counts the files under `dir`, two levels deep.
pub fn count_files(dir: &Path) -> usize {{
    walkdir::WalkDir::new(dir)
        .max_depth(2)
        .into_iter()
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_file())
        .count()
}}
""",
            """        let n = count_files(Path::new(env!("CARGO_MANIFEST_DIR")));
        assert!(n >= 1);""",
        )
    if flavor == "zstd":
        return (
            f"""/// Compresses the JSON form with zstd.
pub fn pack(item: &{i}) -> Result<Vec<u8>, {e}> {{
    zstd::stream::encode_all(item.to_json().as_bytes(), 3)
        .map_err(|err| {e}::Parse("zstd".to_owned(), err.to_string()))
}}

/// Decodes a record that `pack` wrote.
pub fn unpack(data: &[u8]) -> Result<{i}, {e}> {{
    let raw = zstd::stream::decode_all(data)
        .map_err(|err| {e}::Parse("zstd".to_owned(), err.to_string()))?;
    {i}::from_json(&String::from_utf8_lossy(&raw))
}}
""",
            f"""        let it = {i}::new("alpha");
        assert_eq!(unpack(&pack(&it).unwrap()).unwrap(), it);""",
        )
    if flavor == "tar":
        return (
            f"""/// Writes the record as `item.json` in a tar archive.
pub fn archive(item: &{i}) -> Result<Vec<u8>, {e}> {{
    let data = item.to_json();
    let mut h = tar::Header::new_ustar();
    h.set_size(data.len() as u64);
    h.set_mode(0o644);
    h.set_mtime(0);
    let mut b = tar::Builder::new(Vec::new());
    b.append_data(&mut h, "item.json", data.as_bytes())
        .and_then(|()| b.into_inner())
        .map_err(|err| {e}::Parse("tar".to_owned(), err.to_string()))
}}
""",
            f"""        let a = archive(&{i}::new("alpha")).unwrap();
        assert_eq!(a.len() % 512, 0);""",
        )
    if flavor == "ed25519":
        return (
            f"""/// Signs the JSON form with a key made from `seed`.
pub fn sign(item: &{i}, seed: u8) -> [u8; 64] {{
    use ed25519_dalek::Signer;
    let key = ed25519_dalek::SigningKey::from_bytes(&[seed; 32]);
    key.sign(item.to_json().as_bytes()).to_bytes()
}}

/// Verifies a signature that `sign` made.
pub fn verify(item: &{i}, seed: u8, sig: &[u8; 64]) -> bool {{
    let key = ed25519_dalek::SigningKey::from_bytes(&[seed; 32]).verifying_key();
    key.verify_strict(
        item.to_json().as_bytes(),
        &ed25519_dalek::Signature::from_bytes(sig),
    )
    .is_ok()
}}
""",
            f"""        let it = {i}::new("alpha");
        let sig = sign(&it, 7);
        assert!(verify(&it, 7, &sig));
        assert!(!verify(&it, 8, &sig));""",
        )
    if flavor == "ureq":
        return (
            f"""/// The HTTP client of R0.4: ureq with rustls and the ring provider.
pub fn agent(timeout_secs: u64) -> ureq::Agent {{
    ureq::Agent::config_builder()
        .timeout_global(Some(std::time::Duration::from_secs(timeout_secs)))
        .tls_config(
            ureq::tls::TlsConfig::builder()
                .unversioned_rustls_crypto_provider(std::sync::Arc::new(
                    rustls::crypto::ring::default_provider(),
                ))
                .build(),
        )
        .build()
        .into()
}}
""",
            """        let _agent = agent(5);""",
        )
    raise ValueError(flavor)


def module(short, m, n, flavor, dep):
    """The source of module `m{m:02}` (record number n) and its it test."""
    i, e = f"Item{n}", f"Error{n}"
    body, test = flavor_fn(flavor, n)
    uses = ["use std::collections::BTreeMap;", "use std::fmt;", "use std::str::FromStr;"]
    if flavor == "regex":
        uses.insert(1, "use std::sync::LazyLock;")
    if flavor == "walkdir":
        uses.insert(1, "use std::path::Path;")
    uses.sort()
    dep_fn = ""
    if dep:
        dep_fn = f"""
/// Makes a record from a record of `{crate_name(dep)}`.
pub fn from_dep(other: &{ident(dep)}::m00::Item{dep_record(dep)}) -> {i} {{
    let mut it = {i}::new(&other.name);
    it.tags.clone_from(&other.tags);
    it
}}
"""
    marker = "\n/// The lane edit marker. The load test changes its value.\npub const EDIT_MARK: u32 = 0;\n" if m == 0 else ""
    src = f"""//! Generated module {m:02} of `{crate_name(short)}` (spike r0-13).

{chr(10).join(uses)}

use serde::{{Deserialize, Serialize}};
{marker}
/// A record of module {m:02}.
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize, Deserialize)]
pub struct {i} {{
    pub name: String,
    pub version: String,
    pub tags: Vec<String>,
    pub attrs: BTreeMap<String, String>,
}}

/// The errors of module {m:02}.
#[derive(Debug, thiserror::Error)]
pub enum {e} {{
    #[error("cannot parse the {{0}} input: {{1}}")]
    Parse(String, String),
    #[error("the field {{0}} is empty")]
    Missing(&'static str),
}}

impl {i} {{
    /// Makes a record with the default version.
    pub fn new(name: &str) -> Self {{
        Self {{
            name: name.to_owned(),
            version: "0.1.0".to_owned(),
            ..Self::default()
        }}
    }}

    /// Parses a record from JSON.
    pub fn from_json(s: &str) -> Result<Self, {e}> {{
        serde_json::from_str(s).map_err(|err| {e}::Parse("json".to_owned(), err.to_string()))
    }}

    /// Writes the record as JSON.
    pub fn to_json(&self) -> String {{
        serde_json::to_string(self).unwrap_or_default()
    }}

    /// Checks the required fields.
    pub fn validate(&self) -> Result<(), {e}> {{
        if self.name.is_empty() {{
            return Err({e}::Missing("name"));
        }}
        if self.version.is_empty() {{
            return Err({e}::Missing("version"));
        }}
        Ok(())
    }}

    /// The sort key: name, then version.
    pub fn key(&self) -> String {{
        format!("{{}}/{{}}", self.name, self.version)
    }}
}}

impl fmt::Display for {i} {{
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {{
        write!(f, "{{}} {{}} ({{}} tags)", self.name, self.version, self.tags.len())
    }}
}}

impl FromStr for {i} {{
    type Err = {e};

    fn from_str(s: &str) -> Result<Self, Self::Err> {{
        Self::from_json(s)
    }}
}}

/// Merges two sorted lists and removes duplicates.
pub fn merge_sorted<T: Clone + Ord>(a: &[T], b: &[T]) -> Vec<T> {{
    let mut out: Vec<T> = a.iter().chain(b).cloned().collect();
    out.sort();
    out.dedup();
    out
}}

{body}{dep_fn}
#[cfg(test)]
mod tests {{
    use super::*;

    #[test]
    fn json_round_trip() {{
        let mut it = {i}::new("alpha");
        it.attrs.insert("k".to_owned(), "v".to_owned());
        assert_eq!({i}::from_json(&it.to_json()).unwrap(), it);
    }}

    #[test]
    fn validate_rejects_an_empty_name() {{
        assert!({i}::new("").validate().is_err());
        assert!({i}::new("a").validate().is_ok());
    }}

    #[test]
    fn flavor_{flavor}() {{
{test}
    }}
}}
"""
    it = f"""//! Integration tests of module {m:02}.

use {ident(short)}::m{m:02}::{{{i}, merge_sorted}};

#[test]
fn m{m:02}_parse_and_display() {{
    let it = {i}::new("beta");
    let back: {i} = it.to_json().parse().unwrap();
    assert_eq!(back.key(), "beta/0.1.0");
    assert_eq!(back.to_string(), "beta 0.1.0 (0 tags)");
}}

#[test]
fn m{m:02}_merge_sorted() {{
    assert_eq!(merge_sorted(&[1, 3], &[2, 3]), vec![1, 2, 3]);
}}
"""
    return src, it


_RECORD = {}


def dep_record(short):
    return _RECORD[short]


def crate_toml(name, deps_ws, deps_reg, kind="lib"):
    lines = [
        "[package]",
        f'name = "{name}"',
        'version = "0.0.0"',
        "edition.workspace = true",
        "rust-version.workspace = true",
        "publish.workspace = true",
        "",
        "[lints]",
        "workspace = true",
        "",
        "[dependencies]",
    ]
    for d in sorted(deps_reg):
        lines.append(f"{d} = {REG[d]}")
    for d in sorted(deps_ws):
        lines.append(f'{crate_name(d)} = {{ path = "../{crate_name(d)}" }}')
    return "\n".join(lines) + "\n"


def write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)


def gen_cli(out, total_lines):
    if out.exists():
        shutil.rmtree(out)
    weights = sum(w for _, _, w in CRATES)
    names_cli = [s for s, _, _ in CRATES if s != "cli"]
    total_written = 0
    plan = []
    for idx, (short, deps, w) in enumerate(CRATES + EXTRA):
        if deps is None:
            deps = names_cli + ["plugin-sdk"]
        # The kit and SDK extras get a fixed size; they do not count in --lines.
        lines = total_lines * w / weights if idx < len(CRATES) else 300
        nmod = max(1, round(lines / LINES_PER_MODULE))
        plan.append((idx, short, deps, nmod))
    # Record numbers are unique per workspace, so types differ across crates.
    rec = 0
    for idx, short, deps, nmod in plan:
        _RECORD[short] = rec
        rec += nmod
    ws_members = []
    nmods = sum(p[3] for p in plan)
    for idx, short, deps, nmod in plan:
        name = crate_name(short)
        ws_members.append(name)
        d = out / name
        reg = {"serde", "serde_json", "thiserror"}
        mods, its = [], []
        for m in range(nmod):
            flavor = FLAVORS[(idx * 3 + m) % len(FLAVORS)]
            reg.update(FLAVOR_DEPS[flavor])
            dep = deps[m % len(deps)] if deps else None
            src, it = module(short, m, _RECORD[short] + m, flavor, dep)
            write(d / "src" / f"m{m:02}.rs", src)
            write(d / "tests" / "it" / f"m{m:02}.rs", it)
            mods.append(f"pub mod m{m:02};")
            its.append(f"mod m{m:02};")
            total_written += src.count("\n") + it.count("\n")
        link = "".join(
            f"        {ident(dd)}::m00::Item{_RECORD[dd]}::new(\"{dd}\").key(),\n" for dd in deps
        )
        lib = (
            f"//! `{name}`: generated for the r0-13 lane load test. It stands for the\n"
            f"//! crate of the same name in rust-plan.md section 1.\n\n"
            + "\n".join(mods)
            + f"\n\n/// The modules of this crate.\npub const MODULES: usize = {nmod};\n\n"
            + "/// One key from each workspace dependency, so the linker keeps them.\n"
            + "pub fn link() -> Vec<String> {\n"
            + ("    vec![\n" + link + "    ]\n" if deps else "    Vec::new()\n")
            + "}\n"
        )
        write(d / "src" / "lib.rs", lib)
        main_it = (
            f"//! The integration test binary of `{name}`. Units add files here.\n\n"
            + "\n".join(its)
            + f"\n\n#[test]\nfn link_has_one_key_per_dependency() {{\n    assert_eq!({ident(short)}::link().len(), {len(deps)});\n}}\n"
        )
        write(d / "tests" / "it" / "main.rs", main_it)
        total_written += lib.count("\n") + main_it.count("\n")
        write(d / "Cargo.toml", crate_toml(name, deps, reg))
    # spinup: the R0.12 cli-skel binary, linked with spinup-cli.
    sk = SPIKE / "cli-skel"
    d = out / "spinup"
    ws_members.append("spinup")
    shutil.copytree(sk / "src", d / "src")
    shutil.copy(sk / "build.rs", d / "build.rs")
    write(d / "tests" / "it" / "main.rs", (sk / "tests" / "cli.rs").read_text())
    main = (d / "src" / "main.rs").read_text()
    main = main.replace(
        '        "version" => {\n',
        '        "version" => {\n            println!("linked {} crates", spinup_cli::link().len());\n',
        1,
    )
    write(d / "src" / "main.rs", main)
    toml_txt = (sk / "Cargo.toml").read_text().replace('name = "cli-skel"', 'name = "spinup"')
    toml_txt = toml_txt.replace("anyhow.workspace = true", "anyhow.workspace = true\nspinup-cli = { path = \"../spinup-cli\" }")
    write(d / "Cargo.toml", toml_txt)
    # The kit (R0.12 full kit) with its .sqlx data at the workspace root.
    shutil.copytree(SPIKE / "kit", out / "spinup-kit", ignore=shutil.ignore_patterns("target"))
    ws_members.append("spinup-kit")
    klib = out / "spinup-kit" / "src" / "lib.rs"
    klib.write_text(klib.read_text() + "\n/// The lane edit marker. The load test changes its value.\npub const EDIT_MARK: u32 = 0;\n")
    shutil.copytree(SPIKE / ".sqlx", out / ".sqlx")
    shutil.copytree(SPIKE / ".cargo", out / ".cargo")
    root = (SPIKE / "Cargo.toml").read_text()
    root = re.sub(r"members = \[[^\]]*\]", "members = [\n" + "".join(f'    "{m}",\n' for m in sorted(ws_members)) + "]", root, count=1)
    root += """
[workspace.lints.rust]
unsafe_code = "forbid"

[workspace.lints.clippy]
all = { level = "warn", priority = -1 }
"""
    write(out / "Cargo.toml", root)
    shutil.copy(SPIKE / "Cargo.lock", out / "Cargo.lock")
    write(out / ".gitignore", "target/\n")
    lock(out)
    subprocess.run(["cargo", "fmt", "--all"], cwd=out, check=True)
    cli_lines = sum(p.read_text().count("\n") for p in out.rglob("*.rs") if "spinup-kit" not in p.parts[len(out.parts)])
    rs_lines = sum(p.read_text().count("\n") for p in out.rglob("*.rs"))
    print(f"cli workspace {out}: {len(ws_members)} crates, {nmods} generated modules, {cli_lines} CLI lines (45 CLI crates), {rs_lines} Rust lines with the kit and SDK")


def lock(ws):
    env = dict(os.environ, CARGO_NET_OFFLINE="true")
    subprocess.run(["cargo", "update", "--offline", "--workspace", "--quiet"], cwd=ws, env=env, check=True)


# The gate apps: (name, default features, number of resources).
APPS = [
    ("api", ["telemetry"], 1),
    ("auth", ["auth", "telemetry"], 1),
    ("jobs", ["jobs", "telemetry"], 1),
    ("mail", ["auth", "jobs", "telemetry"], 1),
    ("res2", ["auth", "jobs", "telemetry"], 2),
    ("res5", ["auth", "jobs", "telemetry"], 5),
    ("res10", ["auth", "jobs", "telemetry"], 10),
    ("corpus", ["auth", "jobs", "telemetry"], 20),
]

# The dev profile that generated apps ship (R0.11), and the R0.12 release profile.
APP_PROFILES = """
# The dev profile that the generated app ships (R0.11).
[profile.dev]
debug = 0
strip = "symbols"

[profile.dev.package."*"]
opt-level = 1

[profile.dev.package.argon2]
opt-level = 3

[profile.dev.package.blake2]
opt-level = 3

[profile.release]
lto = true
codegen-units = 1
strip = true
panic = "abort"
"""


def ws_tables():
    """The [workspace.package] and [workspace.dependencies] text of R0.11."""
    txt = (R11 / "Cargo.toml").read_text()
    start = txt.index("[workspace.package]")
    end = txt.index("# The R0.12 release profile")
    return txt[start:end].rstrip() + "\n"


def resources():
    """The resource order of R0.11 with each plural (from the migrations)."""
    names = [ln.split()[0] for ln in (R11 / "gen" / "resources.txt").read_text().splitlines() if ln.strip()]
    migs = sorted((R11 / "todo-api" / "migrations").glob("*_create_*.up.sql"))
    plural = {}
    for p in migs:
        mnum, rest = p.name.split("_", 1)
        plural_name = rest[len("create_"):-len(".up.sql")]
        plural[plural_name] = mnum
    out = []
    for n in names:
        cands = [pl for pl in plural if pl.startswith(n[:-1]) and pl not in [o[1] for o in out]]
        cands.sort(key=lambda pl: plural[pl])
        out.append((n, cands[0], plural[cands[0]]))
    return out


def gen_app(dst, name, feats, nres, kit_rel):
    if dst.exists():
        shutil.rmtree(dst)
    src = R11 / "todo-api"
    shutil.copytree(src, dst, ignore=shutil.ignore_patterns("target", "spinup.lock"))
    # openapi.gen.yaml stays: tests/api.rs embeds it. The gate only builds the tests.
    res = resources()
    keep, drop = res[:nres], res[nres:]
    for single, plural, mnum in drop:
        (dst / "src" / "handlers" / f"{plural}.rs").unlink()
        (dst / "src" / "models" / f"{single}.rs").unlink()
        for p in (dst / "migrations").glob(f"{mnum}_*"):
            p.unlink()
    drop_plural = {p for _, p, _ in drop}
    drop_single = {s for s, _, _ in drop}
    for f, names in (("handlers/mod.rs", drop_plural), ("models/mod.rs", drop_single)):
        p = dst / "src" / f
        p.write_text("".join(ln for ln in p.read_text().splitlines(True) if not any(ln == f"pub mod {n};\n" for n in names)))
    p = dst / "src" / "routes_gen.rs"
    p.write_text("".join(ln for ln in p.read_text().splitlines(True) if not any(f"handlers::{n}::routes()" in ln for n in drop_plural)))
    crate = f"app-{name}"
    ident_ = crate.replace("-", "_")
    for p in list((dst / "src").rglob("*.rs")) + list((dst / "tests").rglob("*.rs")):
        p.write_text(p.read_text().replace("todo_api", ident_).replace("todo-api", crate))
    toml_txt = (src / "Cargo.toml").read_text()
    toml_txt = toml_txt.replace('name = "todo-api"', f'name = "{crate}"')
    toml_txt = toml_txt.replace('path = "../kit"', f'path = "{kit_rel}"')
    toml_txt = re.sub(r"default = \[[^\]]*\]", "default = [" + ", ".join(f'"{f}"' for f in feats) + "]", toml_txt, count=1)
    toml_txt += "\n[workspace]\nresolver = \"3\"\n\n" + ws_tables() + APP_PROFILES
    (dst / "Cargo.toml").write_text(toml_txt)
    shutil.copytree(R11 / ".sqlx", dst / ".sqlx")
    shutil.copytree(R11 / ".cargo", dst / ".cargo")
    shutil.copy(R11 / "Cargo.lock", dst / "Cargo.lock")
    lock(dst)


def gen_gate(out):
    if out.exists():
        shutil.rmtree(out)
    kit = out / "kit"
    shutil.copytree(R11 / "kit", kit, ignore=shutil.ignore_patterns("target"))
    lib = kit / "src" / "lib.rs"
    lib.write_text(lib.read_text() + "\n/// The gate edit marker. The load test changes its value.\npub const GATE_EDIT_MARK: u32 = 0;\n")
    # The kit is a path dependency outside the app workspaces. It inherits
    # its versions from its own [workspace] table.
    (kit / "Cargo.toml").write_text((kit / "Cargo.toml").read_text() + "\n[workspace]\nresolver = \"3\"\n\n" + ws_tables())
    shutil.copytree(R11 / ".sqlx", kit / ".sqlx")
    for name, feats, nres in APPS:
        gen_app(out / "apps" / name, name, feats, nres, "../../kit")
    # The reference app for rs-ref-check (the 2-resource tree at another path).
    gen_app(out / "ref" / "todo-rs", "ref", ["auth", "jobs", "telemetry"], 2, "../../kit")
    write(
        out / "docker" / "Dockerfile",
        (SPIKE / "docker" / "Dockerfile.musl").read_text().replace("COPY --from=bin todo-api /api", "COPY --from=bin app-api /api"),
    )
    print(f"gate tree {out}: kit, {len(APPS)} apps, ref app")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("what", choices=["cli", "gate"])
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--lines", type=int, default=25000)
    a = ap.parse_args()
    if a.what == "cli":
        gen_cli(a.out.resolve(), a.lines)
    else:
        gen_gate(a.out.resolve())


if __name__ == "__main__":
    sys.exit(main())
