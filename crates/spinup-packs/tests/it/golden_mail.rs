//! Mail combinations, reset templates, and queued delivery for Rust apps.

use anyhow::Result;
use serde_json::json;

const MAILERS_REGRESSION: &str = r#"

#[cfg(test)]
mod regression_tests {
    use super::*;

    #[tokio::test]
    async fn regression_send_uses_capture() -> anyhow::Result<()> {
        let capture = Arc::new(kit::mail::Capture::new());
        let mailers = Mailers {
            mailer: capture.clone(),
            from: "app@example.com".into(),
            #[cfg(feature = "jobs")]
            transport: capture.clone(),
        };
        let message = Message {
            from: mailers.from.clone(),
            to: "user@example.com".into(),
            subject: "Hello".into(),
            text: "Hello from the app.".into(),
            ..Message::default()
        };
        mailers.send(&message).await?;
        Mailer::send(&mailers, &message).await?;
        assert_eq!(capture.messages(), vec![message.clone(), message]);
        Ok(())
    }

    #[cfg(feature = "jobs")]
    fn lazy_pool() -> kit::db::PgPool {
        sqlx::postgres::PgPoolOptions::new()
            .connect_lazy_with(sqlx::postgres::PgConnectOptions::new_without_pgpass())
    }

    #[cfg(feature = "jobs")]
    #[tokio::test]
    async fn regression_registers_mail_job() -> anyhow::Result<()> {
        let pool = lazy_pool();
        let capture = Arc::new(kit::mail::Capture::new());
        let mailers = Mailers {
            mailer: capture.clone(),
            from: "app@example.com".into(),
            transport: capture.clone(),
        };
        let mut worker = kit::jobs::Worker::new(pool, kit::jobs::Config::default());
        mailers.register_jobs(&mut worker)?;
        assert_eq!(worker.kinds(), vec!["mail.send"]);
        assert!(capture.messages().is_empty());
        Ok(())
    }

    #[cfg(feature = "jobs")]
    #[tokio::test]
    async fn regression_queue_validates_before_database() -> anyhow::Result<()> {
        let mailers = Mailers::configured(
            &lazy_pool(),
            Config {
                base: Base::default(),
                smtp: kit::mail::Config::default(),
                from: "app@example.com".into(),
            },
        )?;
        let error = mailers.send(&Message::default()).await.unwrap_err();
        assert!(error.to_string().contains("a single-line subject"));
        assert!(error.to_string().contains("Try:"));
        Ok(())
    }
}
"#;

#[test]
fn golden_mail() -> Result<()> {
    if !spinup_testutil::golden::selected("mail") {
        return Ok(());
    }
    spinup_testutil::golden::run("mail")?;
    Ok(())
}

#[test]
fn golden_mail_module_combinations() -> Result<()> {
    if !spinup_testutil::golden::selected("mail") {
        return Ok(());
    }
    for (modules, worker, reset) in [
        ("mail", false, false),
        ("auth,mail", false, true),
        ("mail,jobs", true, false),
        ("auth,mail,jobs", true, true),
    ] {
        let mut packs = vec!["base", "api", "mail"];
        if worker {
            packs.push("jobs");
        }
        if reset {
            packs.extend(["web", "auth"]);
        }
        let tree = crate::golden_deploy::render(
            &packs,
            json!({"app_name": "todo", "module": "example.com/todo", "modules": modules}),
        )?;
        assert!(tree.contains("api/src/mail/worker.rs"), "{modules}");
        assert_eq!(tree.contains("api/src/reset.rs"), reset, "{modules}");
        let main = crate::golden_deploy::read(&tree, "api/src/main.rs")?;
        assert!(main.contains("crate::mail::Mailers::new(&pool)?"));
        assert_eq!(main.contains("mailers.register_jobs(worker)?"), worker);
        assert_eq!(
            main.contains("crate::reset::routes(&auth, &mailers, &public_origin)?"),
            reset
        );
        let module = crate::golden_deploy::read(&tree, "api/src/mail/mod.rs")?;
        assert!(module.contains("#[cfg(feature = \"jobs\")]\nmod worker;"));
        assert_eq!(
            module,
            crate::golden_jobs::empty_module(include_str!(
                "../../../spinup-gen-app/templates/mailer/mod.rs.j2"
            ))?
        );
        for suffix in ["subject", "txt", "html"] {
            assert_eq!(
                tree.contains(&format!("api/templates/mail/password_reset.{suffix}.tmpl")),
                reset
            );
        }
        if reset {
            for suffix in ["txt", "html"] {
                let text = crate::golden_deploy::read(
                    &tree,
                    &format!("api/templates/mail/password_reset.{suffix}.tmpl"),
                )?;
                assert!(text.contains("{{ url }}"));
                assert!(!text.contains("{{.URL}}"));
            }
        }
        for (path, _) in tree.iter() {
            assert!(!path.ends_with(".go"), "Go output remains: {path}");
            assert!(
                !path.starts_with("api/internal/"),
                "Go layout remains: {path}"
            );
        }
    }
    Ok(())
}

#[test]
fn golden_mail_login_link_condition() -> Result<()> {
    if !spinup_testutil::golden::selected("mail") {
        return Ok(());
    }
    for enabled in [false, true] {
        let mut packs = vec!["base", "api", "web", "auth"];
        let modules = if enabled {
            packs.push("mail");
            "auth,mail"
        } else {
            "auth"
        };
        let tree = crate::golden_deploy::render(
            &packs,
            json!({"app_name": "todo", "module": "example.com/todo", "modules": modules}),
        )?;
        let login = crate::golden_deploy::read(&tree, "web/src/routes/login/+page.svelte")?;
        assert_eq!(login.contains("Forgot password?"), enabled);
    }
    Ok(())
}

#[test]
fn golden_mail_rendered_regressions() -> Result<()> {
    use spinup_exec::runner::{Cmd, Runner};
    use std::time::Duration;

    if !spinup_testutil::golden::selected("mail") {
        return Ok(());
    }
    let root = spinup_testutil::repo::repo_root()?;
    for (modules, packs) in [
        ("mail", vec!["base", "api", "mail"]),
        (
            "auth,mail,jobs",
            vec!["base", "api", "web", "auth", "mail", "jobs"],
        ),
    ] {
        let tree = crate::golden_deploy::render(
            &packs,
            json!({"app_name": "todo", "module": "example.com/todo", "modules": modules}),
        )?;
        let temp = tempfile::tempdir()?;
        for (path, file) in tree.iter().filter(|(path, _)| path.starts_with("api/")) {
            let dest = temp.path().join(path);
            std::fs::create_dir_all(dest.parent().unwrap())?;
            std::fs::write(dest, &file.data)?;
        }
        let manifest = temp.path().join("api/Cargo.toml");
        let mut text = std::fs::read_to_string(&manifest)?;
        text.push_str(&format!(
            "\n[patch.crates-io]\nspinup-kit = {{ path = {} }}\n",
            toml::Value::String(
                root.join("crates/spinup-kit")
                    .to_string_lossy()
                    .into_owned()
            )
        ));
        std::fs::write(manifest, text)?;
        let mailers = temp.path().join("api/src/mail/mailers.rs");
        let mut text = std::fs::read_to_string(&mailers)?;
        text.push_str(MAILERS_REGRESSION);
        std::fs::write(mailers, text)?;
        let mut cmd = Cmd::new("cargo")
            .args(["test", "--offline", "--locked", "--lib", "regression_"])
            .dir(temp.path().join("api"));
        cmd.timeout = Some(Duration::from_secs(600));
        let target = std::env::var_os("CARGO_TARGET_DIR")
            .map(std::path::PathBuf::from)
            .unwrap_or_else(|| root.join("target"))
            .join("apps");
        cmd.env.push((
            "CARGO_TARGET_DIR".into(),
            target.to_string_lossy().into_owned(),
        ));
        let output = spinup_exec::os_runner::OsRunner
            .output(&cmd, &spinup_exec::runner::Cancellation::new())
            .map_err(|error| {
                anyhow::anyhow!("{modules}: {}", crate::golden_deploy::tool_failure(&error))
            })?;
        assert!(
            output.exit_code == 0,
            "{modules}: {}{}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
        let report = String::from_utf8_lossy(&output.stdout);
        assert!(
            report.contains(if modules == "mail" {
                "1 passed"
            } else {
                "3 passed"
            }),
            "no rendered regressions ran: {report}"
        );
    }
    Ok(())
}
