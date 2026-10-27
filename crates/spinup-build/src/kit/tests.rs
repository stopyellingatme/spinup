use super::*;

#[test]
fn patch_target_and_directory() {
    let root = tempfile::tempdir().unwrap();
    let api = root.path().join("api");
    std::fs::create_dir(&api).unwrap();
    std::fs::create_dir(root.path().join("local kit")).unwrap();
    std::fs::write(
        api.join("Cargo.toml"),
        "[patch.crates-io]\nspinup-kit = { path = '../local kit' }\n",
    )
    .unwrap();
    assert_eq!(
        kit_replace_target(&api).unwrap(),
        Some("../local kit".into())
    );
    assert_eq!(
        kit_replace_dir(&api).unwrap(),
        Some(root.path().join("local kit").canonicalize().unwrap())
    );
    std::fs::remove_dir(root.path().join("local kit")).unwrap();
    assert!(
        kit_replace_dir(&api)
            .unwrap_err()
            .to_string()
            .contains("Try:")
    );
}

#[test]
fn absent_invalid_and_missing_manifest() {
    let root = tempfile::tempdir().unwrap();
    assert!(kit_replace_target(root.path()).is_err());
    for text in [
        "[package]\nname = 'api'",
        "[patch.crates-io]\nspinup-kit = { version = '0.1' }",
    ] {
        std::fs::write(root.path().join("Cargo.toml"), text).unwrap();
        assert_eq!(kit_replace_target(root.path()).unwrap(), None);
    }
    std::fs::write(root.path().join("Cargo.toml"), "[broken").unwrap();
    assert!(
        kit_replace_target(root.path())
            .unwrap_err()
            .to_string()
            .contains("Try:")
    );
}
