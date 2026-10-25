//! The pack digest guard. The milestone gate owns digest updates.

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::path::Path;

use sha2::{Digest, Sha256};
use spinup_vfs::tree::Tree;

#[derive(Debug, PartialEq, Eq)]
struct Record {
    digest: String,
    codemod: String,
}

fn records(data: &str) -> Result<BTreeMap<String, Record>, String> {
    let mut records = BTreeMap::new();
    for (line, text) in data.lines().enumerate() {
        let fields: Vec<_> = text.split_whitespace().collect();
        if fields.is_empty() {
            continue;
        }
        if fields.len() != 3 {
            return Err(format!(
                "packs.digest:{}: want <pack> <digest> <migration-or-none>",
                line + 1
            ));
        }
        let decoded =
            hex::decode(fields[1]).map_err(|_| format!("invalid digest for {}", fields[0]))?;
        if decoded.len() != 32 {
            return Err(format!("invalid digest for {}", fields[0]));
        }
        if records
            .insert(
                fields[0].into(),
                Record {
                    digest: fields[1].into(),
                    codemod: fields[2].into(),
                },
            )
            .is_some()
        {
            return Err(format!("duplicate pack {:?}", fields[0]));
        }
    }
    Ok(records)
}

fn digest(tree: &Tree, pack: &str) -> String {
    fn walk(tree: &Tree, directory: &str, hash: &mut Sha256) {
        for name in tree.children(directory) {
            let path = format!("{directory}/{name}");
            if let Some(file) = tree.get(&path) {
                // Go WalkDir hashes the full path and length before the content.
                // Permission bits do not affect the pack digest.
                hash.update(format!("{path}\0{}\0", file.data.len()).as_bytes());
                hash.update(&file.data);
            } else {
                walk(tree, &path, hash);
            }
        }
    }
    let mut hash = Sha256::new();
    walk(tree, pack, &mut hash);
    hex::encode(hash.finalize())
}

fn updated(tree: &Tree, records: &BTreeMap<String, Record>) -> (String, Vec<String>) {
    let names = crate::loader::list(tree).unwrap();
    let mut output = String::new();
    let mut errors = Vec::new();
    for name in &names {
        let hash = digest(tree, name);
        let previous = records.get(name);
        let decision = match previous {
            Some(previous) if previous.digest == hash => previous.codemod.as_str(),
            _ => {
                errors.push(format!(
                    "pack {name} changed: run make rs-digest UPDATE=1, then review packs.digest"
                ));
                "REVIEW"
            }
        };
        // ADR 0020 fixes the single R1 codemod ID. This crate has no edge to
        // spinup-migrate. Extend this allowlist when a milestone adds codemods.
        if !matches!(decision, "none" | "0001_web_svmigrate") {
            errors.push(format!("pack {name}: {decision:?} needs a registered migration ID or an explicit none decision"));
        }
        writeln!(output, "{name} {hash} {decision}").unwrap();
    }
    let stale: Vec<_> = records
        .keys()
        .filter(|name| !names.contains(name))
        .collect();
    if !stale.is_empty() {
        errors.push(format!("packs.digest lists removed packs: {stale:?}"));
    }
    (output, errors)
}

#[test]
fn packs_digest() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("packs.digest");
    let data = match std::fs::read_to_string(&path) {
        Ok(data) => data,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(error) => panic!("read packs.digest: {error}"),
    };
    let previous = records(&data).unwrap();
    let tree = super::embedded().unwrap();
    let (output, errors) = updated(&tree, &previous);
    if std::env::var("SPINUP_GOLDEN_UPDATE").as_deref() == Ok("1") {
        // This explicit gate action is the only digest write. Lanes exclude it.
        std::fs::write(path, output).unwrap();
    }
    assert!(errors.is_empty(), "{}", errors.join("\n"));
}

#[test]
fn digest_framing_and_order() {
    let mut tree = Tree::new();
    tree.insert("p/z", b"z".to_vec(), 0o644).unwrap();
    tree.insert("p/pack.toml", b"name='p'".to_vec(), 0o644)
        .unwrap();
    tree.insert("p/.hidden", b"hidden".to_vec(), 0o644).unwrap();
    let mut expected = Sha256::new();
    expected.update(b"p/.hidden\x006\x00hiddenp/pack.toml\x008\x00name='p'p/z\x001\x00z");
    assert_eq!(digest(&tree, "p"), hex::encode(expected.finalize()));
    let before = digest(&tree, "p");
    tree.insert("p/z", b"z".to_vec(), 0o755).unwrap();
    tree.insert("other/file", b"ignored".to_vec(), 0o644)
        .unwrap();
    assert_eq!(digest(&tree, "p"), before);
    tree.insert("p/z", b"changed".to_vec(), 0o755).unwrap();
    assert_ne!(digest(&tree, "p"), before);
}

#[test]
fn digest_records_and_decisions() {
    let hash = "0".repeat(64);
    assert!(records(&format!("p {hash} none\np {hash} none\n")).is_err());
    assert!(records("p invalid none").is_err());
    assert!(records("p abcd none").is_err());
    assert!(records("p").is_err());
    let mut tree = Tree::new();
    tree.insert("p/pack.toml", b"name='p'".to_vec(), 0o644)
        .unwrap();
    let hash = digest(&tree, "p");
    for decision in ["none", "0001_web_svmigrate"] {
        let text = format!("p {hash} {decision}\n");
        assert_eq!(updated(&tree, &records(&text).unwrap()), (text, Vec::new()));
    }
    let (output, errors) = updated(&tree, &BTreeMap::new());
    assert_eq!(output, format!("p {hash} REVIEW\n"));
    assert_eq!(errors.len(), 2);
    let stale = records(&format!("removed {hash} none\np {hash} unknown")).unwrap();
    let (_, errors) = updated(&tree, &stale);
    assert!(errors.iter().any(|e| e.contains("removed packs")));
    assert!(errors.iter().any(|e| e.contains("registered migration")));
}

#[test]
fn digest_walk_order() {
    let mut tree = Tree::new();
    tree.insert("p/a.txt", b"file".to_vec(), 0o644).unwrap();
    tree.insert("p/a/x", b"nested".to_vec(), 0o644).unwrap();
    // Go visits directory `a` before file `a.txt`. Full-path sorting differs.
    let expected = Sha256::digest(b"p/a/x\x006\x00nestedp/a.txt\x004\x00file");
    assert_eq!(digest(&tree, "p"), hex::encode(expected));
}
