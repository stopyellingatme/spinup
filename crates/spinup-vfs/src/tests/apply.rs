use super::*;
use crate::{
    diff::diff,
    plan::{File, Plan},
};
use std::{fs, io};

fn plan(files: &[(&str, &[u8], u32, bool)]) -> Plan {
    let mut plan = Plan::new();
    for &(path, data, mode, delete) in files {
        plan.add(File {
            path: path.into(),
            data: data.to_vec(),
            mode,
            delete,
            ..File::default()
        })
        .unwrap();
    }
    plan
}
fn fixture<T: serde::de::DeserializeOwned>(name: &str) -> T {
    serde_json::from_slice(
        &fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/oracle")
                .join(name),
        )
        .unwrap(),
    )
    .unwrap()
}

#[test]
fn dry_run_and_all_change_kinds() {
    let root = tempfile::tempdir().unwrap();
    for (path, data) in [
        ("same", "same\n"),
        ("update", "old\n"),
        ("remove", "remove\n"),
        ("mode", "mode\n"),
    ] {
        fs::write(root.path().join(path), data).unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(root.path().join(path), fs::Permissions::from_mode(0o644)).unwrap();
        }
    }
    let plan = plan(&[
        ("same", b"same\n", 0o644, false),
        ("update", b"new\n", 0o600, false),
        ("remove", b"", 0, true),
        ("nested/create", b"create\n", 0, false),
        ("mode", b"mode\n", 0o600, false),
        ("missing", b"", 0, true),
    ]);
    let dry = apply(root.path(), &plan, ApplyOptions { dry_run: true }).unwrap();
    for change in &dry {
        if matches!(
            change.kind,
            ChangeKind::Create | ChangeKind::Update | ChangeKind::Remove
        ) {
            assert!(!change.unified_diff.is_empty());
        }
    }
    assert_eq!(fs::read(root.path().join("update")).unwrap(), b"old\n");
    assert!(!root.path().join("nested").exists());
    let changes = apply(root.path(), &plan, ApplyOptions::default()).unwrap();
    assert!(changes.iter().all(|c| c.unified_diff.is_empty()));
    assert_eq!(fs::read(root.path().join("update")).unwrap(), b"new\n");
    assert_eq!(
        fs::read(root.path().join("nested/create")).unwrap(),
        b"create\n"
    );
    assert!(!root.path().join("remove").exists());
    for (name, kind) in [
        ("same", ChangeKind::Unchanged),
        ("update", ChangeKind::Update),
        ("remove", ChangeKind::Remove),
        ("nested/create", ChangeKind::Create),
        ("missing", ChangeKind::Unchanged),
    ] {
        assert_eq!(changes.iter().find(|c| c.path == name).unwrap().kind, kind);
    }
    #[cfg(unix)]
    {
        assert_eq!(
            changes.iter().find(|c| c.path == "mode").unwrap().kind,
            ChangeKind::ModeOnly
        );
        assert!(
            diff(root.path(), &plan)
                .unwrap()
                .iter()
                .all(|c| c.kind == ChangeKind::Unchanged)
        );
    }
    assert!(fs::read_dir(root.path()).unwrap().all(|e| {
        !e.unwrap()
            .file_name()
            .to_string_lossy()
            .starts_with(".spinup-vfs-")
    }));
}

#[test]
fn injected_failures_restore_content_modes_deletions_and_directories() {
    for stage in [
        "before-change",
        "before-backup",
        "before-write",
        "before-rename",
        "before-mode",
    ] {
        let root = tempfile::tempdir().unwrap();
        for name in ["a-update", "b-remove", "d-mode", "z-fail"] {
            fs::write(root.path().join(name), b"original").unwrap();
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            for name in ["a-update", "b-remove", "d-mode", "z-fail"] {
                fs::set_permissions(root.path().join(name), fs::Permissions::from_mode(0o775))
                    .unwrap();
            }
        }
        let plan = plan(&[
            ("a-update", b"changed", 0, false),
            ("b-remove", b"", 0, true),
            ("c-dir/sub/create", b"created", 0, false),
            ("d-mode", b"original", 0o600, false),
            (
                "z-fail",
                if stage == "before-mode" {
                    b"original"
                } else {
                    b"changed"
                },
                0o600,
                false,
            ),
        ]);
        let result = apply_with_hook(root.path(), &plan, ApplyOptions::default(), |path, at| {
            if path == "z-fail" && at == stage {
                Err(io::Error::new(io::ErrorKind::PermissionDenied, "injected"))
            } else {
                Ok(())
            }
        });
        let error = result.unwrap_err().to_string();
        assert!(
            error.contains("z-fail") && error.contains("injected") && error.contains("\nTry:"),
            "{stage}: {error}"
        );
        for name in ["a-update", "b-remove", "d-mode", "z-fail"] {
            assert_eq!(
                fs::read(root.path().join(name)).unwrap(),
                b"original",
                "{stage}: {name}"
            );
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                assert_eq!(
                    fs::metadata(root.path().join(name))
                        .unwrap()
                        .permissions()
                        .mode()
                        & 0o777,
                    0o775
                );
            }
        }
        assert!(!root.path().join("c-dir").exists());
        assert_eq!(fs::read_dir(root.path()).unwrap().count(), 4);
    }
}

#[cfg(unix)]
fn symlink(target: &Path, path: &Path, _: bool) {
    std::os::unix::fs::symlink(target, path).unwrap();
}
#[cfg(windows)]
fn symlink(target: &Path, path: &Path, directory: bool) {
    if directory {
        std::os::windows::fs::symlink_dir(target, path).unwrap();
    } else {
        std::os::windows::fs::symlink_file(target, path).unwrap();
    }
}

#[derive(serde::Deserialize)]
struct RootCase {
    setup: String,
    path: String,
    delete: bool,
    diff_ok: bool,
    apply_ok: bool,
}
#[test]
fn rooted_oracle_exercises_diff_and_apply() {
    let cases: Vec<RootCase> = fixture("rooted-paths.json");
    for case in cases {
        let root = tempfile::tempdir().unwrap();
        let outside = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("dir")).unwrap();
        fs::write(root.path().join("dir/file"), b"original\n").unwrap();
        fs::write(outside.path().join("file"), b"outside\n").unwrap();
        let target = match case.setup.as_str() {
            "final-inside" => Some(PathBuf::from("dir/file")),
            "final-outside" => Some(outside.path().join("file")),
            "final-missing" => Some(PathBuf::from("missing")),
            "parent-outside" => Some(outside.path().to_path_buf()),
            "parent-inside" => Some(PathBuf::from("dir")),
            "parent-absolute-inside" => Some(fs::canonicalize(root.path()).unwrap().join("dir")),
            "case-kelvin" => {
                fs::write(root.path().join("dir/K.txt"), b"original\n").unwrap();
                None
            }
            _ => None,
        };
        if let Some(target) = target {
            symlink(
                &target,
                &root.path().join("link"),
                case.setup.starts_with("parent"),
            );
        }
        let plan = plan(&[(&case.path, b"replacement\n", 0, case.delete)]);
        let result = diff(root.path(), &plan);
        assert_eq!(
            result.is_ok(),
            case.diff_ok,
            "diff {}: {result:?}",
            case.setup
        );
        let result = apply(root.path(), &plan, ApplyOptions::default());
        assert_eq!(
            result.is_ok(),
            case.apply_ok,
            "apply {}: {result:?}",
            case.setup
        );
        if let Err(error) = result {
            assert!(error.to_string().contains("\nTry:"));
        }
        assert_eq!(fs::read(outside.path().join("file")).unwrap(), b"outside\n");
    }
}

#[test]
fn rejects_paths_changed_after_comparison_and_rolls_back() {
    for link in [false, true] {
        let root = tempfile::tempdir().unwrap();
        fs::write(root.path().join("a.txt"), b"original").unwrap();
        let plan = plan(&[
            ("a.txt", b"updated", 0, false),
            ("b.txt", b"created", 0, false),
        ]);
        let result = apply_with_hook(
            root.path(),
            &plan,
            ApplyOptions::default(),
            |name, stage| {
                if name == "b.txt" && stage == "before-change" {
                    if link {
                        symlink(Path::new("a.txt"), &root.path().join("b.txt"), false);
                    } else {
                        fs::write(root.path().join("B.txt"), b"original")?;
                    }
                }
                Ok(())
            },
        );
        let error = result.unwrap_err().to_string();
        assert!(error.contains("b.txt") && error.contains("\nTry:"));
        assert_eq!(fs::read(root.path().join("a.txt")).unwrap(), b"original");
        if link {
            assert!(
                fs::symlink_metadata(root.path().join("b.txt"))
                    .unwrap()
                    .is_symlink()
            );
        } else {
            assert_eq!(fs::read(root.path().join("B.txt")).unwrap(), b"original");
        }
    }
}

#[test]
fn invalid_roots_targets_and_raw_plans_report_recovery() {
    let root = tempfile::tempdir().unwrap();
    fs::write(root.path().join("file"), b"data").unwrap();
    fs::create_dir(root.path().join("directory")).unwrap();
    for (target, plan) in [
        (root.path().join("absent"), plan(&[])),
        (root.path().join("file"), plan(&[])),
        (
            root.path().to_path_buf(),
            plan(&[("directory", b"data", 0, false)]),
        ),
        (
            root.path().to_path_buf(),
            Plan {
                files: vec![File {
                    path: "../escape".into(),
                    ..File::default()
                }],
            },
        ),
        (
            root.path().to_path_buf(),
            Plan {
                files: vec![
                    File {
                        path: "README".into(),
                        ..File::default()
                    },
                    File {
                        path: "readme".into(),
                        ..File::default()
                    },
                ],
            },
        ),
    ] {
        assert!(
            diff(&target, &plan)
                .unwrap_err()
                .to_string()
                .contains("\nTry:")
        );
        assert!(
            apply(&target, &plan, ApplyOptions::default())
                .unwrap_err()
                .to_string()
                .contains("\nTry:")
        );
    }
}

#[test]
fn failure_during_parent_creation_removes_only_new_directories() {
    let root = tempfile::tempdir().unwrap();
    fs::create_dir(root.path().join("existing")).unwrap();
    let plan = plan(&[
        ("existing/a/file", b"first", 0, false),
        ("existing/z/file", b"second", 0, false),
    ]);
    let result = apply_with_hook(
        root.path(),
        &plan,
        ApplyOptions::default(),
        |name, stage| {
            if name == "existing/z/file" && stage == "before-change" {
                fs::write(root.path().join("existing/z"), b"obstacle")?;
            }
            Ok(())
        },
    );
    assert!(result.is_err());
    assert!(root.path().join("existing").is_dir());
    assert!(!root.path().join("existing/a").exists());
    assert_eq!(
        fs::read(root.path().join("existing/z")).unwrap(),
        b"obstacle"
    );
}

#[test]
fn unchanged_large_and_binary_files_have_no_diff() {
    let root = tempfile::tempdir().unwrap();
    for data in [vec![0; 9000], vec![b'x'; (1 << 20) + 1]] {
        fs::write(root.path().join("file"), &data).unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(root.path().join("file"), fs::Permissions::from_mode(0o644))
                .unwrap();
        }
        let plan = plan(&[("file", &data, 0, false)]);
        let changes = diff(root.path(), &plan).unwrap();
        assert!(changes[0].unified_diff.is_empty());
        #[cfg(unix)]
        assert_eq!(changes[0].kind, ChangeKind::Unchanged);
    }
}

#[test]
fn temporary_names_do_not_replace_existing_files() {
    let root = tempfile::tempdir().unwrap();
    fs::write(root.path().join(".spinup-vfs-0"), b"keep").unwrap();
    apply(
        root.path(),
        &plan(&[("file", b"new", 0, false)]),
        ApplyOptions::default(),
    )
    .unwrap();
    assert_eq!(
        fs::read(root.path().join(".spinup-vfs-0")).unwrap(),
        b"keep"
    );
    assert_eq!(fs::read_dir(root.path()).unwrap().count(), 2);
}

#[test]
fn internal_absolute_symlinks_in_a_chain_are_readable_and_removable() {
    let root = tempfile::tempdir().unwrap();
    fs::create_dir(root.path().join("actual")).unwrap();
    fs::write(root.path().join("actual/file"), b"old").unwrap();
    symlink(
        &fs::canonicalize(root.path()).unwrap().join("actual"),
        &root.path().join("absolute"),
        true,
    );
    symlink(Path::new("absolute"), &root.path().join("relative"), true);
    let plan = plan(&[("relative/file", b"", 0, true)]);
    diff(root.path(), &plan).unwrap();
    apply(root.path(), &plan, ApplyOptions::default()).unwrap();
    assert!(!root.path().join("actual/file").exists());
}

#[cfg(unix)]
#[test]
fn temporary_file_is_private_before_content_is_written() {
    use std::os::unix::fs::PermissionsExt;
    let root = tempfile::tempdir().unwrap();
    let mut inspected = false;
    apply_with_hook(
        root.path(),
        &plan(&[("file", b"secret", 0o600, false)]),
        ApplyOptions::default(),
        |_, stage| {
            if stage == "before-write" {
                inspected = true;
                let mode = fs::metadata(root.path().join(".spinup-vfs-0"))?
                    .permissions()
                    .mode()
                    & 0o777;
                assert_eq!(mode, 0o600);
            }
            Ok(())
        },
    )
    .unwrap();
    assert!(inspected);
}

#[test]
fn failed_rollback_retains_backups_for_recovery() {
    let root = tempfile::tempdir().unwrap();
    fs::write(root.path().join("a-update"), b"original").unwrap();
    let plan = plan(&[
        ("a-update", b"updated", 0, false),
        ("z-fail", b"new", 0, false),
    ]);
    let error = apply_with_hook(
        root.path(),
        &plan,
        ApplyOptions::default(),
        |name, stage| {
            if name == "z-fail" && stage == "before-change" {
                fs::remove_file(root.path().join("a-update"))?;
                fs::create_dir(root.path().join("a-update"))?;
                return Err(io::Error::other("injected failure"));
            }
            Ok(())
        },
    )
    .unwrap_err()
    .to_string();
    assert!(
        error.contains("Rollback failed")
            && error.contains("Backups remain")
            && error.contains("\nTry:")
    );
    let backup = fs::read_dir(root.path())
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .find(|p| {
            p.file_name()
                .unwrap()
                .to_string_lossy()
                .starts_with(".spinup-vfs-")
        })
        .unwrap();
    assert_eq!(fs::read(backup.join("0")).unwrap(), b"original");
}
