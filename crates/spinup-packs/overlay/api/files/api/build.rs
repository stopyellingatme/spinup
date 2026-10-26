//! Builds a deterministic table of the static web files.

use std::fmt::Write as _;
use std::path::{Path, PathBuf};

fn main() {
    println!("cargo:rerun-if-changed=webdist");
    let root =
        PathBuf::from(std::env::var_os("CARGO_MANIFEST_DIR").expect("Cargo manifest directory"));
    let mut files = Vec::new();
    collect(&root.join("webdist"), &mut files).expect("read webdist files");
    files.sort();
    let mut source = String::from("static WEB_FILES: kit::spa::EmbeddedFiles = &[\n");
    for file in files {
        let name = file
            .strip_prefix(root.join("webdist"))
            .expect("webdist path");
        let name = name.to_str().expect("UTF-8 web path").replace('\\', "/");
        writeln!(
            source,
            "    ({name:?}, include_bytes!({:?})),",
            file.to_str().expect("UTF-8 file path")
        )
        .expect("write file table");
    }
    source.push_str("];\n");
    let out = PathBuf::from(std::env::var_os("OUT_DIR").expect("Cargo output directory"));
    std::fs::write(out.join("webdist.rs"), source).expect("write webdist table");
}

fn collect(dir: &Path, files: &mut Vec<PathBuf>) -> std::io::Result<()> {
    if !dir.exists() {
        return Ok(());
    }
    for entry in std::fs::read_dir(dir)? {
        let entry = entry?;
        if entry.file_name().to_string_lossy().starts_with('.') {
            continue;
        }
        let kind = entry.file_type()?;
        if kind.is_dir() {
            collect(&entry.path(), files)?;
        } else if kind.is_file() {
            files.push(entry.path());
        }
    }
    Ok(())
}
