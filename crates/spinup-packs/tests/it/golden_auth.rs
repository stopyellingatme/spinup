//! Auth schema, conditional pages, and API wiring for Rust apps.

use anyhow::Result;
use serde_json::json;

#[test]
fn golden_auth() -> Result<()> {
    if !spinup_testutil::golden::selected("auth") {
        return Ok(());
    }
    let tree = crate::golden_deploy::render(
        &["base", "api", "web", "auth"],
        json!({"app_name": "todo", "module": "example.com/todo", "modules": "ci,auth"}),
    )?;
    let main = crate::golden_deploy::read(&tree, "api/src/main.rs")?;
    for want in [
        "kit::auth::Auth::new(",
        "secure: !config.is_dev()",
        "origins: vec![public_origin.clone()]",
        ".merge(auth.routes())",
        "auth.layer(routes)",
        "auth.close().await",
        "kit::auth::oidc::routes(",
        "kit::auth::oidc::OidcOptions::load()",
    ] {
        assert!(main.contains(want), "missing auth wiring: {want}");
    }
    let config = spinup_testutil::filter::load()?;
    let render = || -> Result<spinup_vfs::tree::Tree> {
        let case = config.case("auth")?;
        let baseline = spinup_testutil::golden::render_pack(&case.packs, &case.answers)?;
        let mut combined = config.select("auth", &baseline)?;
        let case = config.case("auth/oidc")?;
        let oidc = spinup_testutil::golden::render_pack(&case.packs, &case.answers)?;
        for (path, file) in config.select("auth/oidc", &oidc)?.iter() {
            combined.insert(format!("oidc/{path}"), file.data.clone(), file.mode)?;
        }
        Ok(combined)
    };
    spinup_testutil::golden::deterministic(render)?;
    let files = render()?
        .iter()
        .map(|(path, file)| (path.to_owned(), file.data.to_vec(), file.mode))
        .collect::<Vec<_>>();
    spinup_testutil::golden::compare_rs("auth", &files)?;
    Ok(())
}

#[test]
fn golden_auth_migrations_match_kit_and_conditions() -> Result<()> {
    if !spinup_testutil::golden::selected("auth") {
        return Ok(());
    }
    let root = spinup_testutil::repo::repo_root()?;
    let embedded = spinup_packs::embed::embedded()?;
    let auth = spinup_packs::loader::load(&embedded, "auth")?;
    assert_eq!(auth.requires, ["web"]);
    for file in &auth.files {
        let class = if file.path.starts_with("api/migrations/")
            || file.path.starts_with("api/templates/mail/")
        {
            "seed"
        } else if file.path.starts_with("web/src/lib/server/") || file.path == "api/src/reset.rs" {
            "managed"
        } else {
            "scaffold"
        };
        assert_eq!(file.class, class, "{}", file.path);
    }
    for modules in ["auth", "auth,mail"] {
        for oidc in [false, true] {
            let mut packs = vec!["base", "api", "web", "auth"];
            if modules.contains("mail") {
                packs.push("mail");
            }
            let tree = crate::golden_deploy::render(
                &packs,
                json!({"app_name": "todo", "module": "example.com/todo", "modules": modules, "auth_oidc": oidc}),
            )?;
            for (stem, enabled, condition) in [
                ("00002_create_auth", true, ""),
                (
                    "00003_password_resets",
                    modules.contains("mail"),
                    "modules contains 'mail'",
                ),
                ("00004_oidc_identities", oidc, "auth_oidc"),
            ] {
                for suffix in ["up", "down"] {
                    let name = format!("{stem}.{suffix}.sql");
                    let path = format!("api/migrations/{name}");
                    assert_eq!(
                        tree.contains(&path),
                        enabled,
                        "{modules} OIDC={oidc}: {path}"
                    );
                    let spec = auth.files.iter().find(|f| f.path == path).unwrap();
                    assert_eq!(spec.when, condition, "{path}");
                    let kit = std::fs::read(
                        root.join("crates/spinup-kit/tests/it/migrations")
                            .join(&name),
                    )?;
                    assert_eq!(
                        auth.fs.get(&spec.source).unwrap().data.as_ref(),
                        kit,
                        "{name}"
                    );
                    if enabled {
                        assert_eq!(tree.get(&path).unwrap().data.as_ref(), kit, "{name}");
                    }
                }
            }
            assert_eq!(
                tree.contains("web/src/lib/components/OidcButtons.svelte"),
                oidc
            );
            let login = crate::golden_deploy::read(&tree, "web/src/routes/login/+page.svelte")?;
            assert_eq!(
                login.contains("<OidcButtons next={data.redirectTo} />"),
                oidc
            );
            assert_eq!(login.contains("Forgot password?"), modules.contains("mail"));
            for (path, _) in tree.iter() {
                assert!(!path.ends_with(".go"), "Go output remains: {path}");
                assert!(!path.starts_with("api/db/"), "Go schema remains: {path}");
            }
        }
    }
    Ok(())
}

#[test]
fn golden_auth_off_and_jobs_combination() -> Result<()> {
    if !spinup_testutil::golden::selected("auth") {
        return Ok(());
    }
    let tree = crate::golden_deploy::render(
        &["base", "api", "web"],
        json!({"app_name": "todo", "module": "example.com/todo"}),
    )?;
    let main = crate::golden_deploy::read(&tree, "api/src/main.rs")?;
    let root = spinup_testutil::repo::repo_root()?;
    assert_eq!(
        main,
        std::fs::read_to_string(root.join("testdata/golden-rs/api/api/src/main.rs"))?
    );
    assert!(!main.contains("auth"));
    let both = crate::golden_deploy::render(
        &["base", "api", "web", "auth", "jobs"],
        json!({"app_name": "todo", "module": "example.com/todo", "modules": "auth,jobs"}),
    )?;
    let main = crate::golden_deploy::read(&both, "api/src/main.rs")?;
    assert!(main.contains(".merge(auth.routes())"));
    assert!(main.contains("kit::jobs::worker_command("));
    Ok(())
}
