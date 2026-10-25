//! The tests of `eject` (Go `drift_test.go` and `plugin_test.go`).

use super::*;
use crate::diff::testing::Fixture;

#[test]
fn eject_managed_file_becomes_scaffold() {
    let f = Fixture::new();
    eject(&f.options, "file.txt").unwrap();
    let saved = f.saved();
    assert_eq!(saved.files["file.txt"].class, Class::Scaffold);
    assert_eq!(saved.files["file.txt"].hash, f.lock.files["file.txt"].hash);
}

#[test]
fn eject_dry_run_keeps_lock() {
    let f = Fixture::new();
    let o = Options {
        dry_run: true,
        ..f.options.clone()
    };
    eject(&o, "file.txt").unwrap();
    assert_eq!(f.saved().files["file.txt"].class, Class::Managed);
}

#[test]
fn eject_rejects_paths_that_are_not_managed() {
    let f = Fixture::new();
    eject(&f.options, "file.txt").unwrap();
    for name in ["file.txt", "other.txt"] {
        let text = format!("{:#}", eject(&f.options, name).unwrap_err());
        assert_eq!(
            text,
            format!(
                "cannot eject {name:?}: the path is not managed\nTry: select a managed path with spinup diff --json"
            )
        );
    }
    assert!(eject(&f.options, "../file.txt").is_err());
}
