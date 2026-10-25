//! Builds the three embedded template archives from the Go template packs.
//!
//! Each archive is a deterministic tar (sorted paths, mtime 0, uid 0) of
//! `packs/` plus a `VERSION` file, compressed with zstd level 19. The three
//! versions differ in `VERSION` and in one file each, as consecutive
//! template releases do. `SPINUP_PACKS_DIR` overrides the source directory.

use std::path::{Path, PathBuf};
use std::{env, fs};

use sha2::{Digest, Sha256};

const VERSIONS: [&str; 3] = ["0.1.0", "0.2.0", "0.3.0"];

fn files(root: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    let mut stack = vec![root.to_path_buf()];
    while let Some(dir) = stack.pop() {
        for entry in fs::read_dir(&dir).expect("read the packs directory") {
            let path = entry.expect("read a directory entry").path();
            if path.is_dir() {
                stack.push(path);
            } else {
                out.push(path);
            }
        }
    }
    out.sort();
    out
}

fn append(tar: &mut tar::Builder<Vec<u8>>, name: &str, data: &[u8], mode: u32) {
    let mut h = tar::Header::new_ustar();
    h.set_size(data.len() as u64);
    h.set_mode(mode);
    h.set_mtime(0);
    h.set_uid(0);
    h.set_gid(0);
    h.set_entry_type(tar::EntryType::Regular);
    tar.append_data(&mut h, name, data)
        .expect("append a tar entry");
}

fn main() {
    let manifest = PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap());
    let packs = env::var_os("SPINUP_PACKS_DIR")
        .map(PathBuf::from)
        .unwrap_or_else(|| manifest.join("../../../internal/templates/packs"));
    println!("cargo::rerun-if-env-changed=SPINUP_PACKS_DIR");
    println!("cargo::rerun-if-changed={}", packs.display());
    let list = files(&packs);
    assert!(
        list.len() > 100,
        "found {} files in {}\nTry: set SPINUP_PACKS_DIR to internal/templates/packs",
        list.len(),
        packs.display()
    );
    let out = PathBuf::from(env::var("OUT_DIR").unwrap());
    let mut index = String::from("pub const ARCHIVES: [Archive; 3] = [\n");
    for (i, version) in VERSIONS.iter().enumerate() {
        let mut tar = tar::Builder::new(Vec::new());
        append(
            &mut tar,
            "VERSION",
            format!("{version}\n").as_bytes(),
            0o644,
        );
        for (n, path) in list.iter().enumerate() {
            let rel = path.strip_prefix(&packs).unwrap().to_string_lossy();
            let mut data = fs::read(path).expect("read a pack file");
            // One file changes in each later version.
            if n == i * 37 && i > 0 {
                data.extend_from_slice(format!("\n# changed in {version}\n").as_bytes());
            }
            append(&mut tar, &format!("packs/{rel}"), &data, 0o644);
        }
        let raw = tar.into_inner().expect("finish the tar");
        let zst = zstd::stream::encode_all(raw.as_slice(), 19).expect("compress");
        let name = format!("templates-{version}.tar.zst");
        fs::write(out.join(&name), &zst).expect("write the archive");
        let sum: String = Sha256::digest(&zst)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        index.push_str(&format!(
            "    Archive {{ version: {version:?}, sha256: {sum:?}, tar_bytes: {}, data: include_bytes!(concat!(env!(\"OUT_DIR\"), \"/{name}\")) }},\n",
            raw.len()
        ));
    }
    index.push_str("];\n");
    fs::write(out.join("archives.rs"), index).expect("write the archive index");
}
