use runtime_common::Redactor;

#[test]
fn go_rules_for_every_split_and_byte_chunks() {
    let cases = [
        (vec!["ab", "bcd"], "xabz\n"),
        (vec!["abab"], "xababab"),
        (vec!["ab", "bcd"], "xabcd"),
        (vec!["sec", "secret"], "secret sec secret"),
        (vec!["ab", "bc"], "abcabc"),
        (vec!["aaaa"], "xaaaaaaaayaaa"),
        (vec!["secret"], "secret sec"),
        (vec!["", "secret", "secret"], "secret secret"),
        (vec![], "unchanged\n"),
        (vec!["secret"], ""),
        (vec!["é💡", "💡"], "é💡 and 💡"),
        (vec!["abc", "***"], "abc"),
    ];
    for (mut secrets, input) in cases {
        secrets.sort_by_key(|s| std::cmp::Reverse(s.len()));
        let expected = secrets
            .iter()
            .filter(|s| !s.is_empty())
            .fold(input.to_owned(), |s, secret| s.replace(secret, "***"));
        let secrets: Vec<_> = secrets.iter().map(|s| s.to_string()).collect();
        for split in 0..=input.len() {
            let mut r = Redactor::new(&secrets);
            let mut out = r.push(&input.as_bytes()[..split], false);
            out.extend(r.push(&input.as_bytes()[split..], false));
            out.extend(r.push(&[], true));
            assert_eq!(out, expected.as_bytes(), "{input:?}, split {split}");
        }
        let mut r = Redactor::new(&secrets);
        let mut out = Vec::new();
        for byte in input.bytes() {
            out.extend(r.push(&[byte], false));
        }
        out.extend(r.push(&[], true));
        assert_eq!(out, expected.as_bytes());
    }
}

#[test]
fn partial_secret_is_not_emitted() {
    let mut r = Redactor::new(&["secret".into()]);
    assert!(r.push(b"sec", false).is_empty());
    assert_eq!(r.push(b"ret!", false), b"***!");
}
