use anyhow::{Result, ensure};
use base64::{Engine, engine::general_purpose::STANDARD};
use ed25519_dalek::{Signature, VerifyingKey};
use serde::{Deserialize, Serialize};
use std::io::{Cursor, Read};

#[derive(Debug, PartialEq, Eq, Deserialize, Serialize)]
pub struct Entry {
    pub name: String,
    pub mode: u32,
    #[serde(rename = "type")]
    pub kind: u8,
    pub data: String,
}

/// Verify the raw 64-byte detached signature over compressed archive bytes.
pub fn verify(data: &[u8], signature: &[u8], public: &[u8]) -> Result<()> {
    let key = VerifyingKey::from_bytes(public.try_into()?)?;
    key.verify_strict(data, &Signature::from_slice(signature)?)?;
    Ok(())
}

/// Decode contents and portable metadata for comparison with the Go reader.
pub fn decode(data: &[u8]) -> Result<Vec<Entry>> {
    let decoded = zstd::stream::decode_all(Cursor::new(data))?;
    let mut archive = tar::Archive::new(Cursor::new(decoded));
    let mut entries = Vec::new();
    for entry in archive.entries()? {
        let mut entry = entry?;
        let header = entry.header();
        ensure!(
            header.uid()? == 0 && header.gid()? == 0 && header.mtime()? == 0,
            "unstable metadata"
        );
        ensure!(
            header.username()?.unwrap_or("").is_empty()
                && header.groupname()?.unwrap_or("").is_empty(),
            "unstable owner"
        );
        let name = entry
            .path()?
            .to_str()
            .ok_or_else(|| anyhow::anyhow!("non-UTF8 archive path"))?
            .to_owned();
        let mode = header.mode()?;
        let kind = header.entry_type().as_byte();
        let mut data = Vec::new();
        entry.read_to_end(&mut data)?;
        entries.push(Entry {
            name,
            mode,
            kind,
            data: STANDARD.encode(data),
        });
    }
    Ok(entries)
}

/// Sort entries and write stable USTAR headers followed by single-thread zstd.
pub fn encode(entries: &[Entry]) -> Result<Vec<u8>> {
    let mut sorted: Vec<_> = entries.iter().collect();
    sorted.sort_by(|a, b| a.name.cmp(&b.name));
    let encoder = zstd::stream::Encoder::new(Vec::new(), 3)?;
    let mut tar = tar::Builder::new(encoder);
    for entry in sorted {
        let content = STANDARD.decode(&entry.data)?;
        let mut header = tar::Header::new_ustar();
        header.set_path(&entry.name)?;
        header.set_size(content.len() as u64);
        header.set_mode(entry.mode);
        header.set_uid(0);
        header.set_gid(0);
        header.set_mtime(0);
        header.set_entry_type(tar::EntryType::new(entry.kind));
        header.set_cksum();
        tar.append(&header, content.as_slice())?;
    }
    Ok(tar.into_inner()?.finish()?)
}
