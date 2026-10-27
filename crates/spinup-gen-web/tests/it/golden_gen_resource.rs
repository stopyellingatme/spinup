//! Neutral Go golden trees for resource pages and navigation.
use anyhow::Result;
use spinup_gen::{
    fields::{self, Name},
    part::Spec,
};
use spinup_gen_web::{nav, pages};
use spinup_manifest::lock::Lock;
use spinup_vfs::{plan::Plan, tree::Tree};

fn app(auth: bool) -> Result<(tempfile::TempDir, Lock)> {
    let dir = tempfile::tempdir()?;
    let mut lock = Lock::new();
    lock.packs = ["base", "api", "web"].map(String::from).to_vec();
    let mut answers = serde_json::json!({"app_name":"todo", "module":"example.com/todo"});
    if auth {
        lock.packs.push("auth".into());
        answers["modules"] = serde_json::json!(["auth"]);
    }
    let tree = spinup_testutil::golden::render_pack(&lock.packs, answers.as_object().unwrap())?;
    #[allow(
        clippy::disallowed_methods,
        reason = "Test fixture writes a rendered app in its temporary directory."
    )]
    for (path, file) in tree.iter() {
        let path = dir.path().join(path);
        std::fs::create_dir_all(path.parent().unwrap())?;
        std::fs::write(path, &file.data)?;
    }
    lock.save(dir.path().join("spinup.lock"))?;
    Ok((dir, lock))
}

fn spec<'a>(root: &'a std::path::Path, lock: &'a Lock) -> Spec<'a> {
    Spec {
        root,
        lock,
        name: Name::parse("todo").unwrap(),
        fields: fields::parse(&[
            "title:string",
            "done:bool",
            "due:date?",
            "priority:enum:low|normal|high",
        ])
        .unwrap(),
        owned: lock.packs.iter().any(|p| p == "auth"),
        shared: false,
    }
}

fn golden(name: &str, auth: bool) -> Result<()> {
    if !spinup_testutil::golden::selected(name) {
        return Ok(());
    }
    let (dir, lock) = app(auth)?;
    let render = || -> Result<Tree> {
        let result = pages::plan_pages(&spec(dir.path(), &lock))?;
        assert_eq!(result.post_steps, [spinup_gen::contract::PostStep::Client]);
        nav::plan_nav(dir.path(), result.plan, &[])?.fs()
    };
    let got = render()?;
    spinup_testutil::golden::deterministic(render)?;
    let want = spinup_testutil::golden::read_golden(
        &spinup_testutil::repo::repo_root()?
            .join("testdata/golden")
            .join(name)
            .join("web"),
    )?;
    let mut web = Tree::new();
    for (path, file) in got.iter() {
        web.insert(
            path.strip_prefix("web/").unwrap(),
            file.data.clone(),
            file.mode,
        )?;
    }
    let diff = spinup_testutil::golden::compare_trees(&want, &web);
    assert!(diff.is_empty(), "{}", diff.join("\n"));
    Ok(())
}

#[test]
fn golden_gen_resource_web() -> Result<()> {
    golden("gen-resource", false)
}
#[test]
fn golden_gen_resource_auth_web() -> Result<()> {
    golden("gen-resource-auth", true)
}
#[test]
fn golden_gen_resource_shared_web() -> Result<()> {
    // Go uses --api-only for shared resources, so its tree contains no web files.
    if !spinup_testutil::golden::selected("gen-resource-shared") {
        return Ok(());
    }
    let (dir, lock) = app(true)?;
    let mut shared = spec(dir.path(), &lock);
    shared.shared = true;
    shared.owned = false;
    assert_eq!(
        pages::plan_pages(&shared)?.plan,
        pages::plan_pages(&spec(dir.path(), &lock))?.plan
    );
    Ok(())
}

#[test]
fn golden_gen_resource_paths() -> Result<()> {
    let name = Name::parse("line_item")?;
    let part = spinup_gen::part::RESOURCE_PARTS
        .iter()
        .find(|p| p.name == "pages")
        .unwrap();
    let paths = part.paths.unwrap()(&name);
    assert_eq!(paths.len(), 11);
    assert_eq!(paths[0], "web/src/routes/line-items/+page.server.ts");
    assert!(paths.contains(&"web/src/routes/line-items/form.ts".into()));
    let (dir, lock) = app(false)?;
    let mut s = spec(dir.path(), &lock);
    s.name = name;
    for path in pages::plan_pages(&s)?.plan.paths() {
        assert!(paths.contains(&path));
    }
    assert_eq!(nav::plan_nav(dir.path(), Plan::new(), &[])?.files.len(), 1);
    Ok(())
}

fn generator_output(
    root: &std::path::Path,
    lock: &Lock,
    name: &str,
    args: &[&str],
    flags: &[&str],
) -> Result<spinup_gen::contract::Output> {
    let generator = spinup_gen::contract::GENERATORS
        .iter()
        .find(|g| g.name == name)
        .unwrap();
    let matches = (generator.args)(clap::Command::new("test"))
        .try_get_matches_from(std::iter::once("test").chain(flags.iter().copied()))?;
    let args = args.iter().map(|s| s.to_string()).collect::<Vec<_>>();
    let runner = spinup_exec::fake::Fake::new();
    let cancel = spinup_exec::runner::Cancellation::new();
    (generator.run)(&spinup_gen::contract::Input {
        root,
        lock,
        args: &args,
        matches: &matches,
        runner: &runner,
        cancel: &cancel,
    })
}

#[test]
fn registered_generators_and_flags() -> Result<()> {
    let (dir, mut lock) = app(false)?;
    let root = dir.path();
    let page = generator_output(root, &lock, "page", &["settings/profile"], &["--server"])?;
    assert_eq!(
        page.plan.paths(),
        [
            "web/src/routes/settings/profile/+page.server.ts",
            "web/src/routes/settings/profile/+page.svelte"
        ]
    );
    assert_eq!(page.next, ["cd web && pnpm check"]);
    assert_eq!(page.plan.files[0].class, "scaffold");
    let client = generator_output(root, &lock, "client", &[], &[])?;
    assert_eq!(
        client.post_steps,
        [
            spinup_gen::contract::PostStep::OpenApiEmit,
            spinup_gen::contract::PostStep::Client
        ]
    );
    assert!(
        spinup_gen::run::POST_STEPS
            .iter()
            .any(|s| s.step == spinup_gen::contract::PostStep::Client)
    );
    let c = generator_output(root, &lock, "component", &["dialog"], &[])?;
    assert!(c.plan.files.is_empty());
    assert_eq!(c.commands[0].argv[2], "shadcn-svelte@1.7.0");
    lock.answers.insert(
        "shadcn_registry".into(),
        serde_json::value::RawValue::from_string("\"1.8.0\"".into())?,
    );
    assert_eq!(
        generator_output(root, &lock, "component", &["dialog"], &[])?.commands[0].argv[2],
        "shadcn-svelte@1.8.0"
    );
    lock.answers.insert(
        "package_manager".into(),
        serde_json::value::RawValue::from_string("\"bun\"".into())?,
    );
    let c = generator_output(root, &lock, "component", &["dialog"], &[])?;
    assert_eq!(c.commands[0].argv[0], "bunx");
    assert_eq!(c.next, ["cd web && bun install", "cd web && bun run check"]);
    lock.packs.push("auth".into());
    let page = generator_output(root, &lock, "page", &["private"], &["--server"])?;
    assert!(
        String::from_utf8(
            page.plan
                .get("web/src/routes/private/+page.server.ts")
                .unwrap()
                .data
        )?
        .contains("requireUser(locals, url);")
    );
    lock.answers.insert(
        "frontend_mode".into(),
        serde_json::value::RawValue::from_string("\"embed\"".into())?,
    );
    assert!(generator_output(root, &lock, "page", &["about"], &[]).is_ok());
    assert!(
        generator_output(root, &lock, "page", &["about"], &["--server"])
            .unwrap_err()
            .to_string()
            .contains("embed")
    );
    assert!(
        pages::plan_pages(&spec(root, &lock))
            .unwrap_err()
            .to_string()
            .contains("--api-only")
    );
    Ok(())
}

#[test]
fn generator_refusals_have_try_lines() -> Result<()> {
    let (dir, lock) = app(false)?;
    for (name, args, flags) in [
        ("page", vec![], vec![]),
        ("page", vec!["../x"], vec![]),
        ("component", vec![], vec![]),
        ("component", vec!["Dialog"], vec![]),
        ("client", vec!["extra"], vec![]),
        ("dashboard", vec![], vec![]),
        ("dashboard", vec!["traffic"], vec![]),
        ("alert", vec![], vec![]),
        ("alert", vec!["HighErrors"], vec!["--expr", "up"]),
    ] {
        let error = generator_output(dir.path(), &lock, name, &args, &flags)
            .unwrap_err()
            .to_string();
        assert!(error.contains("\nTry: "), "{name}: {error}");
    }
    let mut lock = lock;
    for raw in ["1", "\"latest\"", "\"1.2.3-beta\"", "null"] {
        lock.answers.insert(
            "shadcn_registry".into(),
            serde_json::value::RawValue::from_string(raw.into())?,
        );
        assert!(
            generator_output(dir.path(), &lock, "component", &["dialog"], &[])
                .unwrap_err()
                .to_string()
                .contains("not a version")
        );
    }
    lock.packs.clear();
    for name in ["page", "component"] {
        assert!(
            generator_output(dir.path(), &lock, name, &["about"], &[])
                .unwrap_err()
                .to_string()
                .contains("no web pack")
        );
    }
    assert!(
        pages::plan_pages(&spec(dir.path(), &lock))?
            .plan
            .files
            .is_empty()
    );
    Ok(())
}

#[test]
fn monitoring_generator_flags() -> Result<()> {
    let root = tempfile::tempdir()?;
    let mut lock = Lock::new();
    lock.packs.push("monitoring".into());
    let d = generator_output(
        root.path(),
        &lock,
        "dashboard",
        &["service-health"],
        &[
            "--panel",
            "Errors=up == 0",
            "--panel",
            "Traffic=rate(requests[5m])",
        ],
    )?;
    let value: serde_json::Value = serde_json::from_slice(&d.plan.files[0].data)?;
    assert_eq!(
        d.plan.files[0].path,
        "monitoring/grafana/dashboards/custom/service-health.json"
    );
    assert_eq!(d.plan.files[0].class, "seed");
    assert_eq!(value["panels"][0]["targets"][0]["expr"], "up == 0");
    assert_eq!(value["panels"][1]["title"], "Traffic");
    let d = generator_output(root.path(), &lock, "dashboard", &["traffic"], &[])?;
    assert!(
        String::from_utf8(d.plan.files[0].data.clone())?
            .contains("http_server_request_duration_seconds_count")
    );
    for (name, args, flags) in [
        ("dashboard", vec!["Traffic"], vec![]),
        ("dashboard", vec!["traffic"], vec!["--panel", "bad"]),
        ("alert", vec!["bad-name"], vec![]),
        ("alert", vec!["HighErrors"], vec![]),
        (
            "alert",
            vec!["HighErrors"],
            vec!["--expr", "up", "--severity", "low"],
        ),
        (
            "alert",
            vec!["HighErrors"],
            vec!["--expr", "up", "--for", "banana"],
        ),
    ] {
        assert!(
            generator_output(root.path(), &lock, name, &args, &flags)
                .unwrap_err()
                .to_string()
                .contains("Try:")
        );
    }
    Ok(())
}

#[test]
#[allow(
    clippy::disallowed_methods,
    reason = "Tests remove inputs from their temporary rendered app."
)]
fn missing_page_inputs_explain_upgrade() -> Result<()> {
    let (dir, lock) = app(false)?;
    std::fs::remove_file(dir.path().join("web/src/lib/components/app/field.svelte"))?;
    std::fs::write(dir.path().join("web/package.json"), "{}")?;
    let e = pages::plan_pages(&spec(dir.path(), &lock))
        .unwrap_err()
        .to_string();
    for text in [
        "field.svelte",
        "sveltekit-superforms",
        "zod",
        "svelte-sonner",
        "spinup upgrade",
    ] {
        assert!(e.contains(text), "{e}");
    }
    for source in [
        "not JSON",
        r#"{"dependencies": "invalid"}"#,
        r#"{"dependencies": {"zod": 7}}"#,
    ] {
        std::fs::write(dir.path().join("web/package.json"), source)?;
        let error = pages::plan_pages(&spec(dir.path(), &lock)).unwrap_err();
        assert!(error.to_string().contains("fix the JSON syntax"));
    }
    Ok(())
}
