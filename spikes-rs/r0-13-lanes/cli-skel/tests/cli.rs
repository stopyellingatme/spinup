//! Tests of the CLI skeleton binary.

use std::process::Command;

fn spinup(args: &[&str]) -> (bool, String, String) {
    let out = Command::new(env!("CARGO_BIN_EXE_spinup"))
        .args(args)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run spinup");
    (
        out.status.success(),
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

#[test]
fn help_lists_the_go_command_groups() {
    let (ok, out, _) = spinup(&["--help"]);
    assert!(ok);
    for group in [
        "new", "generate", "db", "deploy", "backup", "monitor", "upgrade",
    ] {
        assert!(
            out.contains(&format!("  {group} ")),
            "{group} missing:\n{out}"
        );
    }
}

#[test]
fn every_shell_completion_builds_the_full_tree() {
    // clap_complete walks every command, so clap checks the whole tree.
    for shell in ["bash", "zsh", "fish", "powershell"] {
        let (ok, out, err) = spinup(&["completion", shell]);
        assert!(ok, "{shell}: {err}");
        assert!(out.contains("spinup"), "{shell}");
    }
}

#[test]
fn cache_list_decodes_three_archives() {
    let (ok, out, err) = spinup(&["cache", "list", "--json"]);
    assert!(ok, "{err}");
    let rows: serde_json::Value = serde_json::from_str(&out).unwrap();
    let rows = rows.as_array().unwrap();
    assert_eq!(rows.len(), 3);
    for r in rows {
        assert!(r["files"].as_u64().unwrap() > 100, "{r}");
        assert!(r["packs"].as_u64().unwrap() >= 10, "{r}");
    }
    let (ok, out, _) = spinup(&["cache", "list"]);
    assert!(ok);
    assert_eq!(out.lines().count(), 3, "{out}");
}

#[test]
fn new_writes_the_base_pack_and_a_toml_lock() {
    let dir = std::env::temp_dir().join(format!("r0-12-new-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    let (ok, out, err) = spinup(&["-C", dir.to_str().unwrap(), "new", "demo"]);
    assert!(ok, "{err}");
    assert!(out.contains("created"), "{out}");
    let lock = std::fs::read_to_string(dir.join("demo/spinup.lock.toml")).unwrap();
    let lock: toml::Table = toml::from_str(&lock).unwrap();
    assert_eq!(lock["app"].as_str(), Some("demo"));
    assert_eq!(lock["templates"].as_str(), Some("0.3.0"));
    assert!(dir.join("demo/README.md").exists());
    std::fs::remove_dir_all(&dir).unwrap();
}

#[test]
fn new_without_a_name_and_terminal_names_the_argument() {
    let (ok, _, err) = spinup(&["new"]);
    assert!(!ok);
    assert!(err.contains("Try: spinup new <name>"), "{err}");
}

#[test]
fn a_stub_command_parses_its_flags() {
    let (ok, out, err) = spinup(&["deploy", "--plan", "--tag", "x", "--env", "prod"]);
    assert!(ok, "{err}");
    assert_eq!(out.trim(), "spinup deploy: stub (R0.12 skeleton)");
    let (ok, out, _) = spinup(&["db", "migrate", "--i-know", "down"]);
    assert!(ok);
    assert_eq!(out.trim(), "spinup db migrate: stub (R0.12 skeleton)");
}
