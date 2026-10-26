use super::*;

#[test]
fn names_are_pairs_and_ignore_timestamp_numbering() {
    let dir = tempfile::tempdir().unwrap();
    for name in [
        "00007_old.up.sql",
        "00007_old.down.sql",
        "20260101120000_ts.up.sql",
        "009_short.up.sql",
    ] {
        std::fs::write(dir.path().join(name), "").unwrap();
    }
    assert_eq!(
        next_migration_name(dir.path(), "add_widgets").unwrap(),
        (
            "00008_add_widgets.up.sql".into(),
            "00008_add_widgets.down.sql".into()
        )
    );
    assert!(next_migration_name(dir.path(), "old").is_err());
    assert!(next_migration_name(dir.path(), "ts").is_err());
    for bad in ["", "addWidgets", "a__b", "2fa", "../x", "a_"] {
        assert!(next_migration_name(dir.path(), bad).is_err(), "{bad}");
    }
}

#[test]
fn missing_directory_starts_at_one_and_full_directory_fails() {
    let dir = tempfile::tempdir().unwrap();
    assert_eq!(
        next_migration_name(&dir.path().join("missing"), "init")
            .unwrap()
            .0,
        "00001_init.up.sql"
    );
    std::fs::write(dir.path().join("99999_last.down.sql"), "").unwrap();
    assert!(next_migration_name(dir.path(), "next").is_err());
}
