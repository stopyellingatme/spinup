use cap_std::{ambient_authority, fs::Dir};
use r0_05_paths::{clean_path, distinct_paths, rooted_path};
use serde::Deserialize;
use std::{
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicUsize, Ordering},
};

fn fixture<T: serde::de::DeserializeOwned>(name: &str) -> T {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../../testdata/oracle")
        .join(name);
    serde_json::from_slice(&fs::read(path).unwrap()).unwrap()
}

#[derive(Deserialize)]
struct PathCase {
    input: String,
    clean: String,
    valid: bool,
}
#[test]
fn lexical_rules_equal_go() {
    let cases: Vec<PathCase> = fixture("paths.json");
    for case in cases {
        let result = clean_path(&case.input);
        assert_eq!(result.is_ok(), case.valid, "{}", case.input);
        if case.valid {
            assert_eq!(result.unwrap(), case.clean);
        }
    }
}

struct Temp(PathBuf);
impl Temp {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "spinup-r05-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(fs::canonicalize(path).unwrap())
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).unwrap();
    }
}

#[cfg(unix)]
fn symlink(target: &Path, link: &Path, _directory: bool) {
    std::os::unix::fs::symlink(target, link).unwrap();
}
#[cfg(windows)]
fn symlink(target: &Path, link: &Path, directory: bool) {
    // Use a runner with Developer Mode or SeCreateSymbolicLinkPrivilege.
    if directory {
        std::os::windows::fs::symlink_dir(target, link).unwrap();
    } else {
        std::os::windows::fs::symlink_file(target, link).unwrap();
    }
}

#[derive(Deserialize)]
struct RootCase {
    setup: String,
    path: String,
    delete: bool,
    diff_ok: bool,
    apply_ok: bool,
}
#[test]
fn rooted_rules_equal_go() {
    let cases: Vec<RootCase> = fixture("rooted-paths.json");
    for case in cases {
        let root = Temp::new();
        let outside = Temp::new();
        fs::create_dir(root.0.join("dir")).unwrap();
        fs::write(root.0.join("dir/file"), b"original\n").unwrap();
        fs::write(outside.0.join("file"), b"outside\n").unwrap();
        let target = match case.setup.as_str() {
            "final-inside" => Some(PathBuf::from("dir/file")),
            "final-outside" => Some(outside.0.join("file")),
            "final-missing" => Some(PathBuf::from("missing")),
            "parent-outside" => Some(outside.0.clone()),
            "parent-inside" => Some(PathBuf::from("dir")),
            "parent-absolute-inside" => Some(root.0.join("dir")),
            "case-kelvin" => {
                fs::write(root.0.join("dir/K.txt"), b"original\n").unwrap();
                None
            }
            _ => None,
        };
        if let Some(target) = target {
            symlink(
                &target,
                &root.0.join("link"),
                case.setup.starts_with("parent"),
            );
        }
        let dir = Dir::open_ambient_dir(&root.0, ambient_authority()).unwrap();
        let diff = rooted_path(&dir, &root.0, &case.path, false);
        assert_eq!(diff.is_ok(), case.diff_ok, "diff {}: {diff:?}", case.setup);
        let apply = rooted_path(&dir, &root.0, &case.path, !case.delete);
        assert_eq!(
            apply.is_ok(),
            case.apply_ok,
            "apply {} delete={}: {apply:?}",
            case.setup,
            case.delete
        );
        if let Ok(path) = apply {
            if case.delete {
                dir.remove_file(path).unwrap();
            } else {
                dir.write(path, b"replacement\n").unwrap();
            }
        }
        assert_eq!(fs::read(outside.0.join("file")).unwrap(), b"outside\n");
    }
}

#[test]
fn raw_capability_rejects_escape() {
    let root = Temp::new();
    let outside = Temp::new();
    fs::write(outside.0.join("file"), b"outside\n").unwrap();
    symlink(&outside.0, &root.0.join("outside"), true);
    let dir = Dir::open_ambient_dir(&root.0, ambient_authority()).unwrap();
    for name in ["../file", "outside/file"] {
        assert!(dir.open(name).is_err(), "{name}");
    }
    assert!(dir.open(outside.0.join("file")).is_err());
}

#[derive(Deserialize)]
struct PlanCase {
    paths: Vec<String>,
    valid: bool,
}
#[test]
fn planned_case_collisions_equal_go() {
    let cases: Vec<PlanCase> = fixture("plan-paths.json");
    for case in cases {
        assert_eq!(
            distinct_paths(&case.paths).is_ok(),
            case.valid,
            "{:?}",
            case.paths
        );
    }
}
