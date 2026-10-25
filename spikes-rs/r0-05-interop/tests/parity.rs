use base64::{Engine, engine::general_purpose::STANDARD};
use r0_05_interop::{archive, canonical_lock, diff};
use serde::Deserialize;
use serde_json::value::RawValue;
use std::{fs, path::PathBuf};

fn fixture(name: &str) -> Vec<u8> {
    fs::read(
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../testdata/oracle")
            .join(name),
    )
    .unwrap()
}

#[derive(Deserialize)]
struct LockCase {
    name: String,
    input: Box<RawValue>,
    expected: String,
}
#[test]
fn lock_bytes_equal_go() {
    let cases: Vec<LockCase> = serde_json::from_slice(&fixture("locks.json")).unwrap();
    assert!(cases.len() >= 6);
    for case in cases {
        assert_eq!(
            canonical_lock(case.input.get()).unwrap(),
            case.expected,
            "{}",
            case.name
        );
    }
}

#[derive(Deserialize)]
struct DiffCase {
    #[serde(default)]
    encoding: String,
    name: String,
    old: String,
    new: String,
    expected: String,
}
#[test]
fn diff_bytes_equal_go() {
    let cases: Vec<DiffCase> = serde_json::from_slice(&fixture("diffs.json")).unwrap();
    assert!(cases.len() >= 200);
    for case in cases {
        let decode = |data: &str| {
            let raw = STANDARD.decode(data).unwrap();
            if case.encoding == "zstd" {
                zstd::stream::decode_all(raw.as_slice()).unwrap()
            } else {
                raw
            }
        };
        let old = decode(&case.old);
        let new = decode(&case.new);
        // vfs.Diff does not call fileDiff for unchanged files.
        let got = if old == new {
            Vec::new()
        } else {
            diff::file_diff(&case.name, &old, &new)
        };
        assert_eq!(got, decode(&case.expected), "{}", case.name);
    }
}

#[test]
fn signed_archive_and_deterministic_writer() {
    let go = fixture("templates-v1.2.3.tar.zst");
    let signature = fixture("templates-v1.2.3.tar.zst.sig");
    let public = fixture("test-key.pub");
    archive::verify(&go, &signature, &public).unwrap();
    let mut corrupt = go.clone();
    corrupt[20] ^= 1;
    assert!(archive::verify(&corrupt, &signature, &public).is_err());
    let mut corrupt_sig = signature.clone();
    corrupt_sig[0] ^= 1;
    assert!(archive::verify(&go, &corrupt_sig, &public).is_err());
    let expected: Vec<archive::Entry> =
        serde_json::from_slice(&fixture("archive-entries.json")).unwrap();
    assert_eq!(archive::decode(&go).unwrap(), expected);
    let first = archive::encode(&expected).unwrap();
    let second = archive::encode(&expected).unwrap();
    assert_eq!(first, second);
    assert_eq!(archive::decode(&first).unwrap(), expected);
}
