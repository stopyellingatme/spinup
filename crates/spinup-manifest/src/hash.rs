//! Normalized content hashes compatible with Go.
use sha2::{Digest, Sha256};

/// Test the first 8000 bytes for a NUL byte.
pub fn is_binary(data: &[u8]) -> bool {
    data[..data.len().min(8000)].contains(&0)
}

/// Normalize text to LF with one final newline. Preserve empty and binary data.
pub fn normalize(data: &[u8]) -> Vec<u8> {
    if data.is_empty() || is_binary(data) {
        return data.to_vec();
    }
    let mut result = Vec::with_capacity(data.len() + 1);
    let mut bytes = data.iter().peekable();
    while let Some(&byte) = bytes.next() {
        if byte == b'\r' {
            if bytes.peek() == Some(&&b'\n') {
                bytes.next();
            }
            result.push(b'\n');
        } else {
            result.push(byte);
        }
    }
    while result.last() == Some(&b'\n') {
        result.pop();
    }
    result.push(b'\n');
    result
}

/// Return the SHA-256 digest of normalized content with the `sha256:` prefix.
pub fn hash(data: &[u8]) -> String {
    format!("sha256:{}", hex::encode(Sha256::digest(normalize(data))))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn go_normalization_cases() {
        for (input, expected) in [
            (&b""[..], &b""[..]),
            (b"hello", b"hello\n"),
            (b"hello\r\nworld\r\n", b"hello\nworld\n"),
            (b"hello\rworld\r", b"hello\nworld\n"),
            (b"hello\n\n\n", b"hello\n"),
            (b"\r\n\r\n", b"\n"),
            (b"hello \t\n", b"hello \t\n"),
            (b"hello\0\r\n\n", b"hello\0\r\n\n"),
        ] {
            let before = input.to_vec();
            let normalized = normalize(input);
            assert_eq!(normalized, expected);
            assert_eq!(normalize(&normalized), normalized);
            assert_eq!(input, before);
        }
        for position in [7999, 8000, 8001] {
            let mut input = vec![b'a'; position];
            input.push(0);
            assert_eq!(is_binary(&input), position < 8000);
        }
        for input in [
            &b"hello"[..],
            b"hello\n",
            b"hello\r\n",
            b"hello\r",
            b"hello\n\n",
        ] {
            assert_eq!(
                hash(input),
                "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
            );
        }
    }

    #[test]
    fn normalization() {
        assert_eq!(normalize(b"a\r\nb\rc\n\n"), b"a\nb\nc\n");
        assert_eq!(normalize(b""), b"");
        assert_eq!(normalize(b"\n\n"), b"\n");
        assert_eq!(normalize(b"a\0\r\n"), b"a\0\r\n");
        assert_eq!(hash(b"a"), hash(b"a\r\n\r\n"));
        let mut data = vec![b'a'; 8000];
        data.push(0);
        assert!(!is_binary(&data));
        data[7999] = 0;
        assert!(is_binary(&data));
    }
}

#[cfg(test)]
mod oracle_tests {
    use super::*;
    use base64::{Engine as _, engine::general_purpose::STANDARD};
    use serde::Deserialize;

    #[derive(Deserialize)]
    struct Case {
        name: String,
        input: String,
        binary: bool,
        normalized: String,
        hash: String,
    }

    #[test]
    fn oracle_hash_cases() {
        let cases: Vec<Case> =
            serde_json::from_str(include_str!("../../../testdata/oracle/hashes.json")).unwrap();
        for case in cases {
            let bytes = STANDARD.decode(&case.input).unwrap();
            assert_eq!(is_binary(&bytes), case.binary, "{}", case.name);
            assert_eq!(
                normalize(&bytes),
                STANDARD.decode(&case.normalized).unwrap(),
                "{}",
                case.name
            );
            assert_eq!(hash(&bytes), case.hash, "{}", case.name);
        }
    }
}
