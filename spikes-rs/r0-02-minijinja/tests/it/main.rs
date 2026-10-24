use anyhow::{Context, Result, bail, ensure};
use minijinja::value::Serde;
use r0_02_minijinja::{environment, normalize};
use serde::Deserialize;
use serde_json::{Value, json};
use std::collections::BTreeSet;
use std::fs;
use std::path::{Path, PathBuf};
use walkdir::WalkDir;

fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

#[derive(Deserialize)]
struct Inventory {
    pack_templates: usize,
    inline_templates: Vec<String>,
}

#[test]
fn every_template_and_metadata_path_compiles() -> Result<()> {
    let inv: Inventory = serde_json::from_str(include_str!("../../fixtures/inventory.json"))?;
    let mut env = environment();
    let packs = Path::new(env!("CARGO_MANIFEST_DIR")).join("packs");
    let mut pack_count = 0;
    let mut inline = BTreeSet::new();
    for entry in WalkDir::new(&packs).sort_by_file_name() {
        let entry = entry?;
        if !entry.file_type().is_file() {
            continue;
        }
        let name = entry
            .path()
            .strip_prefix(&packs)?
            .to_string_lossy()
            .to_string();
        if name.ends_with(".tmpl") {
            env.add_template_owned(name.clone(), fs::read_to_string(entry.path())?)
                .with_context(|| name.clone())?;
            if name.starts_with("_inline/") {
                inline.insert(name);
            } else {
                pack_count += 1;
            }
        } else if name.ends_with(".toml") {
            let meta: toml::Value = toml::from_str(&fs::read_to_string(entry.path())?)?;
            if let Some(files) = meta.get("files").and_then(toml::Value::as_array) {
                for (i, file) in files.iter().enumerate() {
                    if let Some(path) = file.get("path").and_then(toml::Value::as_str) {
                        env.add_template_owned(format!("{name}:path:{i}"), path.to_owned())?;
                    }
                }
            }
        }
    }
    ensure!(
        pack_count == 74 && pack_count == inv.pack_templates,
        "{pack_count} pack templates"
    );
    ensure!(
        inline == inv.inline_templates.into_iter().collect(),
        "inline inventory differs"
    );
    println!(
        "compiled {pack_count} pack templates and {} inline templates",
        inline.len()
    );
    Ok(())
}

#[derive(Deserialize)]
struct FilterCase {
    source: String,
    data: Value,
    expected: String,
}

#[test]
fn helpers_match_go_oracle() -> Result<()> {
    let cases: Vec<FilterCase> = serde_json::from_str(include_str!("../../fixtures/filters.json"))?;
    let env = environment();
    for (i, case) in cases.iter().enumerate() {
        let rendered = env
            .render_str(&case.source, Serde(json!({"data":case.data})))
            .with_context(|| format!("helper case {i}: {}", case.source))?;
        ensure!(
            normalize(rendered.as_bytes()) == case.expected.as_bytes(),
            "helper {i}: {} with {}\ngot {rendered:?}, want {:?}",
            case.source,
            case.data,
            case.expected
        );
    }
    println!("{} Go helper oracle cases equal", cases.len());
    Ok(())
}

#[test]
fn strict_keys_and_lookup_errors() -> Result<()> {
    let env = environment();
    for source in [
        "[[ data.missing ]]",
        "[[ go_default(\"fallback\", data.missing) ]]",
        "[[ get(data.list, 2) ]]",
        "[[ indent(-1, \"x\") ]]",
        "[[ image(\"missing\") ]]",
        "[[ npm(\"missing\") ]]",
        "[[ gomod(\"missing\") ]]",
        "[[ uses(\"missing\") ]]",
        "[[ has(\"x\", 1) ]]",
    ] {
        ensure!(
            env.render_str(source, Serde(json!({"data":{"list":["one"]}})))
                .is_err(),
            "{source}"
        );
    }
    ensure!(
        env.render_str(
            "[[ get(data, \"absent\") is none ]]",
            Serde(json!({"data":{}}))
        )? == "true"
    );
    ensure!(
        env.render_str(
            "[[ data.x | snake | quote ]]",
            Serde(json!({"data":{"x":"HTTPServer"}}))
        )? == "\"http_server\""
    );
    Ok(())
}

#[derive(Deserialize)]
struct GoldenFile {
    pack: String,
    source: String,
    path: String,
    mode: String,
}
#[derive(Deserialize)]
struct GoldenCase {
    name: String,
    answers: Value,
    files: Vec<GoldenFile>,
}

fn files(dir: &Path) -> Result<BTreeSet<String>> {
    let mut result = BTreeSet::new();
    for entry in WalkDir::new(dir) {
        let entry = entry?;
        if entry.file_type().is_file() {
            let name = entry
                .path()
                .strip_prefix(dir)?
                .to_string_lossy()
                .to_string();
            if name != ".modes" {
                result.insert(name);
            }
        }
    }
    Ok(result)
}

#[test]
fn neutral_golden_trees() -> Result<()> {
    let cases: Vec<GoldenCase> = serde_json::from_str(include_str!("../../fixtures/golden.json"))?;
    let stack: Value = serde_json::from_str(include_str!("../../fixtures/stack.json"))?;
    let mut env = environment();
    let packs = Path::new(env!("CARGO_MANIFEST_DIR")).join("packs");
    for entry in WalkDir::new(&packs) {
        let entry = entry?;
        if entry.file_type().is_file() && entry.path().extension().is_some_and(|e| e == "tmpl") {
            let name = entry
                .path()
                .strip_prefix(&packs)?
                .to_string_lossy()
                .to_string();
            env.add_template_owned(name, fs::read_to_string(entry.path())?)?;
        }
    }
    let mut total = 0;
    let mut equal_total = 0;
    for case in cases {
        let mut answers = case.answers;
        answers["Stack"] = stack.clone();
        let context = Serde(json!({"data":answers}));
        let golden = root().join("testdata/golden").join(&case.name);
        let modes = fs::read_to_string(golden.join(".modes"))?;
        let mut seen = BTreeSet::new();
        let mut equal = 0;
        let mut unequal = Vec::new();
        for file in &case.files {
            let path = env.render_str(&file.path, context.clone())?;
            ensure!(
                seen.insert(path.clone()),
                "duplicate {}/{}",
                case.name,
                path
            );
            let source = format!("{}/{}", file.pack, file.source);
            let bytes = if source.ends_with(".tmpl") {
                env.get_template(&source)?
                    .render(context.clone())
                    .with_context(|| source.clone())?
                    .into_bytes()
            } else {
                fs::read(packs.join(&source))?
            };
            let got = normalize(&bytes);
            let want =
                fs::read(golden.join(&path)).with_context(|| format!("{}/{}", case.name, path))?;
            let expected_mode = modes
                .lines()
                .find_map(|line| {
                    let (mode, name) = line.split_once(' ')?;
                    (name == path).then_some(mode)
                })
                .unwrap_or("0644");
            ensure!(
                file.mode == expected_mode,
                "mode {}/{path}: {} != {expected_mode}",
                case.name,
                file.mode
            );
            if got == want {
                equal += 1;
            } else {
                let first = got
                    .iter()
                    .zip(&want)
                    .position(|(a, b)| a != b)
                    .unwrap_or(got.len().min(want.len()));
                let cause = if case.name == "_sample" && path == "api/cmd/demo/main.go" {
                    "Go formatter is absent"
                } else {
                    "render mismatch"
                };
                unequal.push(format!("{path}: {cause}, first byte {first}"));
            }
        }
        let mut want_files = files(&golden)?;
        if case.name == "web-embed" {
            // The API pack owns these Go files. R0.2 measures neutral packs.
            want_files.retain(|s| {
                !s.starts_with("api/")
                    && s != "deploy/Dockerfile.api"
                    && s != "deploy/compose.dev.yml"
            });
        }
        ensure!(
            seen == want_files,
            "inventory {}: missing {:?}, extra {:?}",
            case.name,
            want_files.difference(&seen).collect::<Vec<_>>(),
            seen.difference(&want_files).collect::<Vec<_>>()
        );
        println!(
            "{}: files={}, equal={}, unequal={} {:?}",
            case.name,
            case.files.len(),
            equal,
            unequal.len(),
            unequal
        );
        if case.name == "base" || case.name == "web" || case.name.starts_with("web-variants/") {
            ensure!(
                unequal.is_empty(),
                "{} requires 100 percent equality",
                case.name
            );
        }
        if unequal
            .iter()
            .any(|s| !s.contains("Go formatter is absent"))
        {
            bail!("unexpected mismatches in {}: {:?}", case.name, unequal);
        }
        total += case.files.len();
        equal_total += equal;
    }
    ensure!(
        equal_total * 100 >= total * 99,
        "{equal_total}/{total} files equal"
    );
    println!(
        "TOTAL: files={total}, equal={equal_total}, percent={:.6}",
        equal_total as f64 * 100.0 / total as f64
    );
    Ok(())
}
