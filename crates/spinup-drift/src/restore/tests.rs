//! The tests of `restore` (Go `drift_test.go` and `plugin_test.go`).

use spinup_manifest::lock::Class;

use super::*;
use crate::diff::testing::Fixture;

#[test]
fn restore_tombstone_writes_file_and_entry() {
    let mut f = Fixture::new();
    f.lock.tombstone("file.txt");
    f.save();
    std::fs::remove_file(f.root().join("file.txt")).unwrap();
    restore(&f.options, "file.txt", false).unwrap();
    assert_eq!(
        std::fs::read(f.root().join("file.txt")).unwrap(),
        b"template\n"
    );
    let saved = f.saved();
    assert!(saved.tombstones.is_empty());
    let want = FileEntry {
        class: Class::Managed,
        template: "base/file.txt".into(),
        hash: hash(b"template\n"),
        mode: "0644".into(),
    };
    assert_eq!(saved.files["file.txt"], want);
}

#[test]
fn restore_keeps_tracked_class_and_needs_force() {
    let f = Fixture::new();
    crate::eject::eject(&f.options, "file.txt").unwrap();
    f.write("file.txt", "edited\n");
    let text = format!("{:#}", restore(&f.options, "file.txt", false).unwrap_err());
    assert!(
        text.ends_with(
            "the existing file differs from the template\nTry: spinup restore file.txt --force"
        ),
        "{text}"
    );
    let dry = Options {
        dry_run: true,
        ..f.options.clone()
    };
    restore(&dry, "file.txt", true).unwrap();
    assert_eq!(
        std::fs::read(f.root().join("file.txt")).unwrap(),
        b"edited\n"
    );
    restore(&f.options, "file.txt", true).unwrap();
    assert_eq!(
        std::fs::read(f.root().join("file.txt")).unwrap(),
        b"template\n"
    );
    assert_eq!(f.saved().files["file.txt"].class, Class::Scaffold);
}

#[test]
fn restore_rejects_unknown_and_untracked_paths() {
    let mut f = Fixture::new();
    let text = format!("{:#}", restore(&f.options, "other.txt", false).unwrap_err());
    assert!(
        text.contains("the template does not contain this path"),
        "{text}"
    );
    f.lock.files.clear();
    f.save();
    let text = format!("{:#}", restore(&f.options, "file.txt", false).unwrap_err());
    assert!(
        text.contains("the path is not tracked or tombstoned"),
        "{text}"
    );
    let text = format!("{:#}", restore(&f.options, "../outside", true).unwrap_err());
    assert!(text.contains("parent paths are not allowed"), "{text}");
}

#[cfg(unix)]
#[test]
fn restore_never_writes_through_a_symlink() {
    let f = Fixture::new();
    std::fs::remove_file(f.root().join("file.txt")).unwrap();
    let outside = tempfile::tempdir().unwrap();
    let target = outside.path().join("outside");
    std::fs::write(&target, b"keep").unwrap();
    std::os::unix::fs::symlink(&target, f.root().join("file.txt")).unwrap();
    assert!(restore(&f.options, "file.txt", true).is_err());
    assert_eq!(std::fs::read(&target).unwrap(), b"keep");
}

#[cfg(unix)]
#[test]
fn restore_never_writes_through_a_symlinked_parent() {
    let mut f = Fixture::new();
    f.lock.tombstone("file.txt");
    f.save();
    let outside = tempfile::tempdir().unwrap();
    // The project root itself is reached through a link: canonical paths still compare.
    let link = outside.path().join("link");
    std::os::unix::fs::symlink(f.root(), &link).unwrap();
    let o = Options {
        dir: link,
        ..f.options.clone()
    };
    restore(&o, "file.txt", true).unwrap();
    assert!(f.saved().tombstones.is_empty());
}
