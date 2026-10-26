//! The `secrets` tests of the kit (Go `kit/secrets/secrets_test.go`).

use std::io;
use std::path::Path;

use spinup_kit::secrets;

fn load(file: Option<&str>, direct: Option<&str>) -> anyhow::Result<String> {
    let file = file.map(str::to_string);
    let direct = direct.map(str::to_string);
    let has_file = file.is_some();
    secrets::load_from(
        "TEST_SECRET",
        move |key| match key {
            "TEST_SECRET_FILE" if has_file => Some("/run/secrets/test".into()),
            "TEST_SECRET" => direct.clone(),
            _ => None,
        },
        move |path: &Path| {
            assert_eq!(path, Path::new("/run/secrets/test"));
            file.clone()
                .map(String::into_bytes)
                .ok_or_else(|| io::Error::from(io::ErrorKind::NotFound))
        },
    )
}

#[test]
fn secrets_load() {
    for (name, file, direct, want) in [
        ("file wins", Some("file\n"), Some("environment"), "file"),
        ("one newline", Some("file\n\n"), None, "file\n"),
        ("windows newline", Some("file\r\n"), None, "file"),
        ("environment", None, Some("environment\n"), "environment"),
        ("empty", None, Some(""), ""),
    ] {
        assert_eq!(load(file, direct).unwrap(), want, "{name}");
    }
    let err = load(None, None).unwrap_err().to_string();
    assert_eq!(
        err,
        "read secret TEST_SECRET: no file or environment value is set\nTry: set TEST_SECRET_FILE or TEST_SECRET"
    );
}

#[test]
fn secrets_unreadable_file_does_not_fall_back() {
    let err = secrets::load_from(
        "TEST_SECRET",
        |key| match key {
            "TEST_SECRET_FILE" => Some("/missing".into()),
            "TEST_SECRET" => Some("do-not-expose".into()),
            _ => None,
        },
        |_| Err(io::Error::from(io::ErrorKind::NotFound)),
    )
    .unwrap_err()
    .to_string();
    assert!(!err.contains("do-not-expose"), "{err}");
    assert!(err.contains("cannot read TEST_SECRET_FILE\nTry: "), "{err}");
}

#[test]
#[should_panic(expected = "read secret SPINUP_KIT_TEST_SECRET_NEVER_SET")]
fn secrets_must_panics_without_the_value() {
    secrets::must("SPINUP_KIT_TEST_SECRET_NEVER_SET");
}
