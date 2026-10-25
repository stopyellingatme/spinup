//! Binary detection with the Go VFS scan boundary.

/// Returns true when the first 8,000 bytes contain a NUL byte.
pub fn is_binary(data: &[u8]) -> bool {
    data[..data.len().min(8000)].contains(&0)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn scan_boundary() {
        assert!(!is_binary(b""));
        assert!(!is_binary(b"\xff\xfe"));
        let mut data = vec![b'x'; 8001];
        data[8000] = 0;
        assert!(!is_binary(&data));
        data[7999] = 0;
        assert!(is_binary(&data));
    }
}
