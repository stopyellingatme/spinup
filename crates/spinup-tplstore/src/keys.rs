//! Embedded trusted keys and detached Ed25519 verification.

use anyhow::{Result, bail};
use ed25519_dalek::{Signature, VerifyingKey};

/// The signing keys compiled into this build.
pub const TRUSTED_KEYS: &str = include_str!("../trusted_keys.txt");

// Keep this check in a source module: build.rs is outside this unit's file set.
// option_env! also makes Cargo track changes to SPINUP_RELEASE.
const _: () = {
    if let Some(value) = option_env!("SPINUP_RELEASE")
        && value.len() == 1
        && value.as_bytes()[0] == b'1'
    {
        let bytes = TRUSTED_KEYS.as_bytes();
        let mut at_start = true;
        let mut comment = false;
        let mut has_key = false;
        let mut i = 0;
        while i < bytes.len() {
            let c = bytes[i];
            if c == b'\n' {
                at_start = true;
                comment = false;
            } else if at_start && c == b'#' {
                comment = true;
                at_start = false;
            } else if !comment && c != b' ' && c != b'\r' && c != b'\t' {
                has_key = true;
                at_start = false;
            }
            i += 1;
        }
        assert!(
            has_key,
            "SPINUP_RELEASE=1 requires a key in trusted_keys.txt"
        );
    }
};

/// Parses one lowercase hex public key per line. Comments start with `#`.
pub fn parse(text: &str) -> Result<Vec<VerifyingKey>> {
    let mut keys = Vec::new();
    for (index, line) in text.lines().enumerate() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.len() != 64
            || !line
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        {
            bail!(
                "cannot read template signing key on line {}: expected 64 lowercase hex characters\nTry: correct trusted_keys.txt and rebuild spinup",
                index + 1
            );
        }
        let mut bytes = [0; 32];
        hex::decode_to_slice(line, &mut bytes)?;
        keys.push(VerifyingKey::from_bytes(&bytes).map_err(|error| anyhow::anyhow!(
            "cannot read template signing key on line {}: {error}\nTry: correct trusted_keys.txt and rebuild spinup", index + 1
        ))?);
    }
    Ok(keys)
}

/// Returns the trusted keys embedded in this build.
pub fn trusted_keys() -> Result<Vec<VerifyingKey>> {
    parse(TRUSTED_KEYS)
}

/// Verifies a raw 64-byte signature over the compressed archive bytes.
pub fn verify(data: &[u8], signature: &[u8], keys: &[VerifyingKey]) -> Result<()> {
    if keys.is_empty() {
        bail!(
            "no trusted template signing keys are configured in this build\nTry: install a spinup build with trusted release keys"
        );
    }
    if let Ok(signature) = Signature::from_slice(signature)
        && keys
            .iter()
            .any(|key| key.verify_strict(data, &signature).is_ok())
    {
        return Ok(());
    }
    bail!("template archive signature is invalid\nTry: use a valid signed templates archive");
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_signature_and_tampering() {
        let data = include_bytes!("../../../testdata/oracle/templates-v1.2.3.tar.zst");
        let signature = include_bytes!("../../../testdata/oracle/templates-v1.2.3.tar.zst.sig");
        let key = ed25519_dalek::VerifyingKey::from_bytes(include_bytes!(
            "../../../testdata/oracle/test-key.pub"
        ))
        .unwrap();
        verify(data, signature, &[key]).unwrap();
        assert!(verify(b"tampered", signature, &[key]).is_err());
        assert!(verify(data, &[0; 64], &[key]).is_err());
        assert!(verify(data, signature, &[]).is_err());
        assert!(verify(data, &[0; 65], &[key]).is_err());
    }

    #[test]
    fn key_file_rejects_invalid_lines() {
        assert!(parse("# comment\n\n").unwrap().is_empty());
        for text in ["00", &"A".repeat(64), &"g".repeat(64)] {
            assert!(parse(text).is_err());
        }
        let key = hex::encode(include_bytes!("../../../testdata/oracle/test-key.pub"));
        assert_eq!(parse(&format!("# keys\n{key}\n")).unwrap().len(), 1);
    }
}
