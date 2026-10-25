use anyhow::{Context, Result};
use r0_05_interop::archive::{self, Entry};
use std::{fs, path::PathBuf};

fn main() -> Result<()> {
    let output = std::env::args_os()
        .nth(1)
        .context("usage: write-archive OUTPUT.tar.zst")?;
    let input = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/oracle/archive-entries.json");
    let entries: Vec<Entry> = serde_json::from_slice(&fs::read(input)?)?;
    fs::write(output, archive::encode(&entries)?)?;
    Ok(())
}
