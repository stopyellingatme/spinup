//! The tests of `diff` (Go `drift_test.go` and `plugin_test.go`).

use spinup_manifest::lock::Plugin;
use spinup_tplstore::source::SourceError;
use spinup_ui::printer::Printer;

use super::testing::{Fixture, printer};
use super::*;

fn run(o: &Options, paths: &[&str]) -> (Result<()>, String) {
    let (p, out): (Printer, _) = printer();
    let paths: Vec<String> = paths.iter().map(|p| p.to_string()).collect();
    let result = diff(o, &paths, &p);
    (result, out.contents())
}

#[test]
fn diff_clean_project_prints_nothing() {
    let f = Fixture::new();
    let (result, out) = run(&f.options, &[]);
    result.unwrap();
    assert_eq!(out, "");
}

#[test]
fn diff_modified_prints_unified_diff() {
    let f = Fixture::new();
    f.write("file.txt", "edited\n");
    let (result, out) = run(&f.options, &[]);
    result.unwrap();
    assert_eq!(
        out,
        "diff a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-template\n+edited\n"
    );
}

#[test]
fn diff_binary_files_differ() {
    let f = Fixture::new();
    f.write("file.txt", "a\0b");
    let (result, out) = run(&f.options, &[]);
    result.unwrap();
    assert_eq!(out, "file.txt: binary files differ\n");
}

#[test]
fn diff_states_and_json() {
    let mut f = Fixture::new();
    f.lock.tombstone("file.txt");
    f.lock.files.insert(
        "gone.txt".into(),
        spinup_manifest::lock::FileEntry {
            class: spinup_manifest::lock::Class::Seed,
            template: "base/gone.txt".into(),
            hash: spinup_manifest::hash::hash(b"x"),
            mode: "0644".into(),
        },
    );
    f.save();
    let (result, out) = run(&f.options, &[]);
    result.unwrap();
    assert_eq!(
        out,
        "tombstone\tmanaged\tfile.txt\nmissing\tseed\tgone.txt\n"
    );

    let o = Options {
        json: true,
        ..f.options.clone()
    };
    let (result, out) = run(&o, &["gone.txt"]);
    result.unwrap();
    assert_eq!(
        out,
        "{\n  \"files\": [\n    {\n      \"path\": \"gone.txt\",\n      \"class\": \"seed\",\n      \"state\": \"missing\"\n    }\n  ]\n}\n"
    );
}

#[test]
fn diff_untracked_rendered_file() {
    let mut f = Fixture::new();
    f.lock.files.clear();
    f.save();
    let (result, out) = run(&f.options, &[]);
    result.unwrap();
    assert_eq!(out, "untracked\tmanaged\tfile.txt\n");
}

#[test]
fn diff_selects_paths_and_their_children() {
    assert!(selected("a/b.txt", &[]));
    assert!(selected("a/b.txt", &["a".into()]));
    assert!(selected("a/b.txt", &["a/b.txt".into()]));
    assert!(!selected("ab/c.txt", &["a".into()]));
    assert!(!selected("a.txt", &["a".into()]));
    let f = Fixture::new();
    f.write("file.txt", "edited\n");
    let (result, out) = run(&f.options, &["other"]);
    result.unwrap();
    assert_eq!(out, "");
    let (result, _) = run(&f.options, &["../file.txt"]);
    assert!(format!("{:#}", result.unwrap_err()).contains("parent paths are not allowed"));
}

#[test]
fn diff_unavailable_template_keeps_source_error() {
    let f = Fixture::new();
    let (result, _) = run(&f.unavailable(), &[]);
    let err = result.unwrap_err();
    let source = err.downcast_ref::<SourceError>().unwrap();
    assert!(source.is_unavailable());
    assert!(format!("{err:#}").ends_with("Try: spinup cache warm"));
}

#[test]
fn diff_outside_project_fails_with_try() {
    let dir = tempfile::tempdir().unwrap();
    let o = Options {
        dir: dir.path().into(),
        ..Options::default()
    };
    let (result, _) = run(&o, &[]);
    let text = format!("{:#}", result.unwrap_err());
    assert!(text.starts_with("cannot locate application: "), "{text}");
    assert!(
        text.ends_with("Try: run inside a spinup project or pass --dir"),
        "{text}"
    );
}

#[test]
fn diff_locked_plugins_need_plugin_support() {
    let mut f = Fixture::new();
    f.lock.plugins.push(Plugin {
        name: "example".into(),
        version: "v1.0.0".into(),
        sha256: "aa".into(),
        api: 1,
    });
    f.save();
    let (result, _) = run(&f.options, &[]);
    let text = format!("{:#}", result.unwrap_err());
    assert!(text.contains("the project declares example"), "{text}");
    assert!(text.contains("\nTry: "), "{text}");
}

#[cfg(unix)]
#[test]
fn diff_rejects_non_regular_tracked_file() {
    let f = Fixture::new();
    std::fs::remove_file(f.root().join("file.txt")).unwrap();
    std::os::unix::fs::symlink(f.root().join("spinup.toml"), f.root().join("file.txt")).unwrap();
    let (result, _) = run(&f.options, &[]);
    let text = format!("{:#}", result.unwrap_err());
    assert!(
        text.starts_with("cannot inspect application files: "),
        "{text}"
    );
}
