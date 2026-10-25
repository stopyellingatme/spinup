//! The Go tests of `internal/doctor/plainsecrets`, ported.

use super::*;
use spinup_exec::fake::Fake;

struct Dir(std::path::PathBuf);

impl Dir {
    fn new(name: &str) -> Self {
        let path =
            std::env::temp_dir().join(format!("spinup-doctor-ps-{}-{name}", std::process::id()));
        let _ = std::fs::remove_dir_all(&path);
        std::fs::create_dir_all(&path).unwrap();
        Self(path)
    }
}

impl Drop for Dir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn context(root: &Dir, fake: &Fake) -> Context {
    let mut ctx = Context::new(&root.0);
    ctx.runner = Some(Arc::new(fake.clone()));
    ctx
}

#[test]
fn plain_secrets() {
    let ok = ".env\n.env.*\n";
    let tests: &[(&str, &str, &str, &str, Status)] = &[
        (
            "encrypted",
            "secrets/staging.enc.yaml",
            "sops:\n  age: []\n",
            ok,
            Status::Ok,
        ),
        (
            "multiple documents",
            "secrets/plain.yaml",
            "sops:\n  age: []\n---\nPASSWORD: do-not-expose\n",
            ok,
            Status::Fail,
        ),
        (
            "empty metadata",
            "secrets/plain.yaml",
            "sops: {}\nPASSWORD: do-not-expose\n",
            ok,
            Status::Fail,
        ),
        (
            "plaintext",
            "secrets/plain.yaml",
            "PASSWORD: do-not-expose\n",
            ok,
            Status::Fail,
        ),
        (
            "fake metadata",
            "secrets/plain.yaml",
            "PASSWORD: 'text sops: do-not-expose'\n",
            ok,
            Status::Fail,
        ),
        (
            "age key",
            "keys/recovery.agekey",
            "AGE-SECRET-KEY-do-not-expose",
            ok,
            Status::Fail,
        ),
        (
            "dotenv",
            "api/.env",
            "PASSWORD=do-not-expose",
            ok,
            Status::Fail,
        ),
        (
            "dotenv variant",
            "api/.env.production",
            "PASSWORD=do-not-expose",
            ok,
            Status::Fail,
        ),
        (
            "dotenv example",
            "api/.env.example",
            "PASSWORD=example",
            ok,
            Status::Ok,
        ),
        (
            "missing ignore",
            "README.md",
            "safe",
            "node_modules/\n",
            Status::Fail,
        ),
    ];
    for (name, tracked, content, ignore, want) in tests {
        let root = Dir::new(&name.replace(' ', "-"));
        let path = root.0.join(tracked);
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(&path, content).unwrap();
        std::fs::write(root.0.join(".gitignore"), ignore).unwrap();
        let fake = Fake::new();
        fake.expect("git", ["ls-files", "-z"])
            .returns(format!("{tracked}\0"), "", 0);
        if *name == "encrypted" {
            fake.expect("git", ["show".to_string(), format!(":./{tracked}")])
                .returns(content, "", 0);
        }
        let result = run_check(&context(&root, &fake));
        assert_eq!(result.status, *want, "{name}: {result:?}");
        assert!(!result.message.contains("do-not-expose"), "{name}");
        assert!(
            result.status != Status::Fail || !result.try_hint.is_empty(),
            "{name}"
        );
    }
}

#[test]
fn quick_skips() {
    assert_eq!(run_check(&Context::default()).status, Status::Skip);
    let root = Dir::new("quick");
    let mut ctx = Context::new(&root.0);
    ctx.quick = true;
    assert_eq!(run_check(&ctx).status, Status::Skip);
}

#[test]
fn staged_plaintext() {
    let root = Dir::new("staged");
    std::fs::create_dir(root.0.join("secrets")).unwrap();
    std::fs::write(
        root.0.join("secrets/staging.enc.yaml"),
        "sops:\n  age: []\n",
    )
    .unwrap();
    std::fs::write(root.0.join(".gitignore"), ".env\n.env.*\n").unwrap();
    let fake = Fake::new();
    fake.expect("git", ["ls-files", "-z"])
        .returns("secrets/staging.enc.yaml\0", "", 0);
    fake.expect("git", ["show", ":./secrets/staging.enc.yaml"])
        .returns("PASSWORD: do-not-expose\n", "", 0);
    let result = run_check(&context(&root, &fake));
    assert_eq!(result.status, Status::Fail);
    assert!(result.message.contains("staged plaintext"));
    assert!(!result.message.contains("do-not-expose"));
    fake.assert_done().unwrap();
}

#[test]
fn unsafe_git_path() {
    let root = Dir::new("unsafe");
    let fake = Fake::new();
    fake.expect("git", ["ls-files", "-z"])
        .returns("../x\0", "", 0);
    let result = run_check(&context(&root, &fake));
    assert_eq!(result.message, "Git returned an unsafe path");
}

#[test]
fn registered_check() {
    let check = crate::registry::checks(Scope::Local, false)
        .into_iter()
        .find(|c| c.id == "plaintext-secrets")
        .expect("plaintext-secrets is registered");
    assert!(!check.quick);
}

#[test]
fn glob_matches_like_go() {
    for (pattern, name, want) in [
        (".env", ".env", true),
        (".env.*", ".env.local", true),
        (".env*", ".env", true),
        ("*.env", ".env", true),
        ("?env", ".env", true),
        ("[.]env", ".env", true),
        ("[^.]env", ".env", false),
        ("[a-z]env", ".env", false),
        ("\\.env", ".env", true),
        ("a/b", ".env", false),
        ("[", ".env", false),
        ("node_modules/", ".env", false),
    ] {
        assert_eq!(glob_match(pattern, name), want, "{pattern} {name}");
    }
}

#[test]
fn ignore_rules() {
    assert!(ignores_dotenv(".env\n.env.*\n"));
    assert!(ignores_dotenv("**/.env\n**/.env.local\n"));
    assert!(ignores_dotenv("/.env\n/.env.local\n"));
    assert!(!ignores_dotenv(".env\n.env.*\n!.env.local\n"));
    assert!(!ignores_dotenv(".env\n"));
}
