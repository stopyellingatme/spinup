//! The R0.10 acceptance checks of the generator. They need `rustfmt` (the
//! pinned toolchain has it) and no database.

use std::fs;

use spinup_gen::compare::{
    REFERENCE, TempDir, copy_base, generate, not_rustfmt_clean, plan_one, workspace,
};
use spinup_gen::{Class, LOCK, Lock, Plan, apply, hash, tree_hash};

/// The committed app is the output of the chosen plan (C): a fresh run on
/// the base app gives the same bytes and the same lock.
#[test]
fn committed_app_is_current() {
    let dir = TempDir::new("current").unwrap();
    copy_base(dir.path()).unwrap();
    generate(dir.path(), REFERENCE, Plan::Rustfmt).unwrap();
    let lock: Lock = serde_json::from_slice(&fs::read(dir.path().join(LOCK)).unwrap()).unwrap();
    let committed = workspace().join("todo-api");
    for path in lock.files.keys().map(String::as_str).chain([LOCK]) {
        let want = fs::read(committed.join(path)).unwrap();
        let got = fs::read(dir.path().join(path)).unwrap();
        assert!(
            want == got,
            "{path} differs from the generator output\nTry: cargo run -p gen --bin gen -- resource ... --app todo-api --force"
        );
    }
}

/// A second run on the committed app writes nothing.
#[test]
fn second_run_on_the_committed_app_changes_nothing() {
    let dir = TempDir::new("again").unwrap();
    let committed = workspace().join("todo-api");
    copy_base(dir.path()).unwrap();
    let lock: Lock = serde_json::from_slice(&fs::read(committed.join(LOCK)).unwrap()).unwrap();
    for path in lock.files.keys().map(String::as_str).chain([LOCK]) {
        let to = dir.path().join(path);
        fs::create_dir_all(to.parent().unwrap()).unwrap();
        fs::copy(committed.join(path), to).unwrap();
    }
    for report in generate(dir.path(), REFERENCE, Plan::Rustfmt).unwrap() {
        assert!(report.written.is_empty(), "{:?}", report.written);
    }
}

/// For each plan: the tree hash is equal over two fresh runs, and a second
/// run on the same app writes nothing.
#[test]
fn every_plan_is_deterministic_and_idempotent() {
    for plan in Plan::ALL {
        let mut hashes = Vec::new();
        for _ in 0..2 {
            let dir = TempDir::new(plan.name()).unwrap();
            copy_base(dir.path()).unwrap();
            generate(dir.path(), REFERENCE, plan).unwrap();
            hashes.push(tree_hash(dir.path()).unwrap());
            for report in generate(dir.path(), REFERENCE, plan).unwrap() {
                assert!(report.written.is_empty(), "{plan:?}: {:?}", report.written);
            }
        }
        assert_eq!(hashes[0], hashes[1], "{plan:?}");
    }
}

/// Plans A and C give rustfmt-clean, equal bytes for the reference
/// resources. Plan B does not: prettyplease output is not rustfmt output.
#[test]
fn rustfmt_cleanliness_of_each_plan() {
    let dir = TempDir::new("clean").unwrap();
    copy_base(dir.path()).unwrap();
    for args in REFERENCE {
        let a = plan_one(dir.path(), args, Plan::Stable).unwrap();
        let b = plan_one(dir.path(), args, Plan::PrettyPlease).unwrap();
        let c = plan_one(dir.path(), args, Plan::Rustfmt).unwrap();
        assert_eq!(
            not_rustfmt_clean(dir.path(), &a).unwrap(),
            Vec::<String>::new()
        );
        assert_eq!(
            not_rustfmt_clean(dir.path(), &c).unwrap(),
            Vec::<String>::new()
        );
        assert_eq!(a, c, "the templates are rustfmt-stable for {}", args[0]);
        assert!(!not_rustfmt_clean(dir.path(), &b).unwrap().is_empty());
        apply(dir.path(), &c, false).unwrap();
    }
}

/// The rustfmt-stable templates break on a longer name: rustfmt joins or
/// splits a signature. Plan C stays clean.
#[test]
fn stable_templates_break_on_long_names() {
    let dir = TempDir::new("long").unwrap();
    copy_base(dir.path()).unwrap();
    let args = ["order_line", "title:string", "done:bool"];
    let a = plan_one(dir.path(), &args, Plan::Stable).unwrap();
    assert_eq!(
        not_rustfmt_clean(dir.path(), &a).unwrap(),
        ["src/handlers/order_lines.rs"]
    );
    let c = plan_one(dir.path(), &args, Plan::Rustfmt).unwrap();
    assert!(not_rustfmt_clean(dir.path(), &c).unwrap().is_empty());
}

/// Generator-owned files list each resource once, sorted. Migrations get the
/// next number, and a second run of a resource keeps its number.
#[test]
fn owned_files_and_migration_numbers() {
    let dir = TempDir::new("owned").unwrap();
    copy_base(dir.path()).unwrap();
    generate(dir.path(), &[REFERENCE[1], REFERENCE[0]], Plan::Rustfmt).unwrap();
    let read = |p: &str| fs::read_to_string(dir.path().join(p)).unwrap();
    assert!(read("src/handlers/mod.rs").ends_with("\npub mod notes;\npub mod todos;\n"));
    assert!(read("src/models/mod.rs").ends_with("\npub mod note;\npub mod todo;\n"));
    let routes = read("src/routes_gen.rs");
    let notes = routes.find("handlers::notes::routes()").unwrap();
    assert!(notes < routes.find("handlers::todos::routes()").unwrap());
    assert!(
        dir.path()
            .join("migrations/00002_create_notes.up.sql")
            .exists()
    );
    assert!(
        dir.path()
            .join("migrations/00003_create_todos.down.sql")
            .exists()
    );
    let again = plan_one(dir.path(), REFERENCE[1], Plan::Rustfmt).unwrap();
    assert!(
        again
            .iter()
            .any(|f| f.path == "migrations/00002_create_notes.up.sql")
    );
}

/// A changed scaffold file stops the run (ADR 0007 §1) unless `force` is
/// set. A changed generator-owned file is rewritten, and its lock record
/// keeps the hash of the written bytes.
#[test]
fn apply_respects_file_classes() {
    let dir = TempDir::new("classes").unwrap();
    copy_base(dir.path()).unwrap();
    generate(dir.path(), &REFERENCE[..1], Plan::Rustfmt).unwrap();
    let handlers = dir.path().join("src/handlers/todos.rs");
    fs::write(&handlers, "// my edit\n").unwrap();
    let routes = dir.path().join("src/routes_gen.rs");
    fs::write(&routes, "// stale\n").unwrap();
    let files = plan_one(dir.path(), REFERENCE[0], Plan::Rustfmt).unwrap();
    let err = apply(dir.path(), &files, false).unwrap_err().to_string();
    assert!(err.contains("src/handlers/todos.rs exists"), "{err}");
    assert!(err.contains("\nTry: pass --force"), "{err}");
    assert_eq!(fs::read_to_string(&handlers).unwrap(), "// my edit\n");

    // Without the edit to the scaffold file, only the owned file changes.
    let planned = files
        .iter()
        .find(|f| f.path == "src/handlers/todos.rs")
        .unwrap();
    fs::write(&handlers, &planned.contents).unwrap();
    let report = apply(dir.path(), &files, false).unwrap();
    assert_eq!(report.written, ["src/routes_gen.rs"]);
    let lock: Lock = serde_json::from_slice(&fs::read(dir.path().join(LOCK)).unwrap()).unwrap();
    let entry = &lock.files["src/routes_gen.rs"];
    assert_eq!(entry.class, Class::Managed);
    assert_eq!(entry.hash, hash(&fs::read(&routes).unwrap()));

    fs::write(&handlers, "// my edit\n").unwrap();
    let report = apply(dir.path(), &files, true).unwrap();
    assert_eq!(report.written, ["src/handlers/todos.rs"]);
}
