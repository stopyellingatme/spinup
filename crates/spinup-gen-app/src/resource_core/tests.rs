use super::*;
use spinup_exec::{fake::Fake, runner::Cancellation};
use spinup_gen::part::PartPlan;
use spinup_manifest::lock::Lock;
use spinup_vfs::plan::{File, Plan};

fn args(values: &[&str]) -> Vec<String> {
    values.iter().map(|s| (*s).into()).collect()
}

#[test]
fn spec_refusals_have_retry_hints() {
    let root = tempfile::tempdir().unwrap();
    let lock = Lock::new();
    for (values, message) in [
        (vec!["todo"], "at least one field"),
        (vec!["todo", "title"], "name:type"),
        (vec!["todo", "owner:ref:member"], "no migration"),
        (vec!["category", "parent:ref:category"], "must be nullable"),
    ] {
        let error = parse_spec(root.path(), &lock, &args(&values), "resource").unwrap_err();
        assert!(error.to_string().contains(message), "{error}");
        assert!(error.to_string().contains("\nTry:"), "{error}");
    }
    for name in ["doc", "docs", "Doc", "openapi", "openapis", "Openapi"] {
        let error = parse_spec(
            root.path(),
            &lock,
            &args(&[name, "title:string"]),
            "resource",
        )
        .unwrap_err();
        assert!(error.to_string().contains("reserved for the API reference"));
    }
}

#[test]
fn required_columns_match_go() {
    let columns = sql::required_columns(
        "id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
         name text NOT NULL,
         size text NOT NULL CHECK (size IN ('s', 'm')),
         note text,
         org_id bigint NOT NULL REFERENCES orgs (id),
         amount numeric(10, 2) NOT NULL,
         created_at timestamptz NOT NULL DEFAULT now(),
         CONSTRAINT x UNIQUE (name)",
    );
    let actual: Vec<_> = columns
        .iter()
        .map(|c| (c.name.as_str(), c.value.as_str(), c.reference.as_str()))
        .collect();
    assert_eq!(
        actual,
        [
            ("name", "'sample'", ""),
            ("size", "'s'", ""),
            ("org_id", "", "orgs"),
            ("amount", "1", ""),
        ]
    );
}

#[allow(
    clippy::disallowed_methods,
    reason = "tests create isolated app fixtures"
)]
fn write(root: &Path, path: &str, source: &str) {
    let path = root.join(path);
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, source).unwrap();
}

fn text(plan: &Plan, path: &str) -> String {
    String::from_utf8(plan.get(path).unwrap().data).unwrap()
}

fn file(path: &str) -> File {
    File {
        path: path.into(),
        class: "scaffold".into(),
        ..File::default()
    }
}

fn generate(root: &Path, lock: &Lock, values: &[&str], flags: &[&str]) -> Result<Output> {
    let matches = (RESOURCE.args)(clap::Command::new("resource"))
        .arg(
            clap::Arg::new("force")
                .long("force")
                .action(clap::ArgAction::SetTrue),
        )
        .try_get_matches_from(std::iter::once("resource").chain(flags.iter().copied()))?;
    run(&Input {
        root,
        lock,
        args: &args(values),
        matches: &matches,
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
}

#[test]
fn migration_pair_has_stable_sql_and_seed_class() {
    let root = tempfile::tempdir().unwrap();
    write(root.path(), "api/migrations/00001_init.up.sql", "SELECT 1;");
    let lock = Lock::new();
    let spec = parse_spec(
        root.path(),
        &lock,
        &args(&[
            "todo",
            "title:string",
            "done:bool",
            "due:date?",
            "parent:ref:todo?",
            "state:enum:draft|sent",
        ]),
        "resource",
    )
    .unwrap();
    let plan = sql::plan_migration(&spec, false).unwrap();
    assert_eq!(
        plan.paths(),
        [
            "api/migrations/00002_create_todos.down.sql",
            "api/migrations/00002_create_todos.up.sql",
        ]
    );
    assert!(plan.files.iter().all(|f| f.class == "seed"));
    assert_eq!(
        text(&plan, "api/migrations/00002_create_todos.up.sql"),
        concat!(
            "CREATE TABLE todos (\n",
            "    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n",
            "    title text NOT NULL,\n",
            "    done boolean NOT NULL,\n",
            "    due date,\n",
            "    parent_id bigint REFERENCES todos (id),\n",
            "    state text NOT NULL CHECK (state IN ('draft', 'sent')),\n",
            "    created_at timestamptz NOT NULL DEFAULT now(),\n",
            "    updated_at timestamptz NOT NULL DEFAULT now()\n",
            ");\n",
            "CREATE INDEX todos_parent_id_idx ON todos (parent_id);\n",
        )
    );
    assert_eq!(
        text(&plan, "api/migrations/00002_create_todos.down.sql"),
        "DROP TABLE todos;\n"
    );
    assert!(
        !root
            .path()
            .join("api/migrations/00002_create_todos.up.sql")
            .exists()
    );
}

#[test]
fn force_reuses_only_generated_migration_pairs() {
    for (name, generated) in [("00002_create_todos", true), ("00002_schema", false)] {
        let root = tempfile::tempdir().unwrap();
        write(
            root.path(),
            &format!("api/migrations/{name}.up.sql"),
            "CREATE TABLE todos (id bigint);",
        );
        write(
            root.path(),
            "api/migrations/00003_index.up.sql",
            "SELECT 1;",
        );
        let lock = Lock::new();
        let spec = parse_spec(
            root.path(),
            &lock,
            &args(&["todo", "title:string"]),
            "resource",
        )
        .unwrap();
        let error = sql::plan_migration(&spec, false).unwrap_err();
        assert!(error.to_string().contains("Try: pass --force"));
        let plan = sql::plan_migration(&spec, true).unwrap();
        assert_eq!(plan.files.len(), if generated { 2 } else { 0 });
        if generated {
            assert!(
                plan.get("api/migrations/00002_create_todos.down.sql")
                    .is_some()
            );
        }
    }
}

#[test]
fn migration_name_failure_keeps_retry_hint() {
    let root = tempfile::tempdir().unwrap();
    write(
        root.path(),
        "api/migrations/00002_create_todos.down.sql",
        "DROP TABLE todos;",
    );
    let lock = Lock::new();
    let error = generate(root.path(), &lock, &["todo", "title:string"], &[]).unwrap_err();
    assert!(
        error.to_string().contains("already has the name"),
        "{error}"
    );
    assert!(error.to_string().contains("\nTry:"), "{error}");
}

#[test]
fn find_table_reads_only_up_files_and_real_definitions() {
    let root = tempfile::tempdir().unwrap();
    assert!(sql::find_table(root.path(), "members").unwrap().is_none());
    write(
        root.path(),
        "api/migrations/00001_init.down.sql",
        "CREATE TABLE members (wrong text);",
    );
    write(
        root.path(),
        "api/migrations/00001_init.sql",
        "CREATE TABLE members (wrong text);",
    );
    write(
        root.path(),
        "api/migrations/00001_init.up.sql",
        concat!(
            "-- CREATE TABLE members (wrong text);\n",
            "/* CREATE TABLE members (wrong text); */\n",
            "SELECT 'CREATE TABLE members (wrong text)';\n",
            "CREATE TABLE memberships (wrong text);\n",
        ),
    );
    write(
        root.path(),
        "api/migrations/00003_later.up.sql",
        "CREATE TABLE members (later text);",
    );
    write(
        root.path(),
        "api/migrations/00002_members.up.sql",
        concat!(
            "CREATE TABLE IF NOT EXISTS \"public\".\"members\" (\n",
            "id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n",
            "size numeric(10,2) NOT NULL, -- comment\n",
            "label text NOT NULL CHECK (label IN ('a,b', 'it''s)'))\n",
            ");\nCREATE TABLE unrelated (id bigint);",
        ),
    );
    let def = sql::find_table(root.path(), "members").unwrap().unwrap();
    assert_eq!(def.file, "00002_members.up.sql");
    assert!(!def.body.contains("comment"));
    assert!(!def.body.contains("unrelated"));
    assert_eq!(sql::required_columns(&def.body)[1].value, "'a,b'");
}

#[test]
fn parents_follow_dependencies_and_skip_self_and_duplicates() {
    let root = tempfile::tempdir().unwrap();
    write(
        root.path(),
        "api/migrations/00002_orgs.up.sql",
        "CREATE TABLE orgs (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);",
    );
    write(
        root.path(),
        "api/migrations/00003_members.up.sql",
        "CREATE TABLE members (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, org_id bigint NOT NULL REFERENCES public.orgs (id), active boolean NOT NULL);",
    );
    let lock = Lock::new();
    let spec = parse_spec(
        root.path(),
        &lock,
        &args(&[
            "todo",
            "owner:ref:member",
            "other:ref:member?",
            "parent:ref:todo?",
        ]),
        "resource",
    )
    .unwrap();
    assert_eq!(
        sql::parent_inserts(root.path(), &spec).unwrap(),
        [
            sql::ParentInsert {
                table: "orgs".into(),
                sql: "INSERT INTO orgs (name) VALUES ('sample')".into()
            },
            sql::ParentInsert {
                table: "members".into(),
                sql:
                    "INSERT INTO members (org_id, active) VALUES ((SELECT max(id) FROM orgs), true)"
                        .into()
            },
        ]
    );
}

#[test]
fn parents_bound_cycles_and_use_defaults_for_missing_dependencies() {
    let root = tempfile::tempdir().unwrap();
    write(
        root.path(),
        "api/migrations/00001_members.up.sql",
        "CREATE TABLE members (org_id bigint NOT NULL REFERENCES orgs(id), missing_id bigint NOT NULL REFERENCES missing(id)); CREATE TABLE orgs (member_id bigint NOT NULL REFERENCES members(id));",
    );
    let lock = Lock::new();
    let spec = parse_spec(
        root.path(),
        &lock,
        &args(&["todo", "owner:ref:member"]),
        "resource",
    )
    .unwrap();
    let parents = sql::parent_inserts(root.path(), &spec).unwrap();
    assert_eq!(
        parents.iter().map(|p| p.table.as_str()).collect::<Vec<_>>(),
        ["orgs", "missing", "members"]
    );
    assert_eq!(parents[1].sql, "INSERT INTO missing DEFAULT VALUES");
}

#[test]
fn every_field_type_has_correct_nullability() {
    let root = tempfile::tempdir().unwrap();
    let lock = Lock::new();
    for ty in fields::types().into_iter().filter(|ty| ty.name != "ref") {
        let kind = if ty.name == "enum" {
            "enum:a|b"
        } else {
            &ty.name
        };
        for nullable in [false, true] {
            let value = format!("value:{kind}{}", if nullable { "?" } else { "" });
            let spec =
                parse_spec(root.path(), &lock, &args(&["gadget", &value]), "resource").unwrap();
            let plan = sql::plan_migration(&spec, false).unwrap();
            let up = text(&plan, "api/migrations/00001_create_gadgets.up.sql");
            let line = up
                .lines()
                .find(|line| line.starts_with("    value "))
                .unwrap();
            assert_eq!(line.contains("NOT NULL"), !nullable, "{line}");
            assert!(line.contains(&ty.sql), "{line}");
        }
    }
}

#[test]
fn required_columns_sample_all_supported_sql_types() {
    let columns = sql::required_columns(concat!(
        "a text NOT NULL, b bigint PRIMARY KEY, c boolean NOT NULL, ",
        "d timestamp NOT NULL, e date NOT NULL, f uuid NOT NULL, ",
        "g jsonb NOT NULL, h numeric(10, 2) NOT NULL, ",
        "i custom NOT NULL, j text DEFAULT 'x' NOT NULL, k bigserial PRIMARY KEY, ",
        "l text, CONSTRAINT p PRIMARY KEY (a), ",
        "m text NOT NULL CHECK (m IN ('it''s', 'other'))",
    ));
    let values: Vec<_> = columns
        .iter()
        .map(|c| (c.name.as_str(), c.value.as_str()))
        .collect();
    assert_eq!(
        values,
        [
            ("a", "'sample'"),
            ("b", "1"),
            ("c", "true"),
            ("d", "'2026-01-02T03:04:05Z'"),
            ("e", "'2026-01-02'"),
            ("f", "'00000000-0000-4000-8000-000000000001'"),
            ("g", "'{}'"),
            ("h", "1"),
            ("i", ""),
            ("m", "'it''s'"),
        ]
    );
}

#[test]
fn ownership_follows_input_lock_and_shared_flag() {
    for (auth, shared) in [(false, false), (true, false), (true, true), (false, true)] {
        let root = tempfile::tempdir().unwrap();
        // The runner supplies the validated lock. The generator must use that lock.
        write(root.path(), "spinup.lock", "invalid");
        let mut lock = Lock::new();
        if auth {
            lock.packs.push("auth".into());
        }
        let flags: &[&str] = if shared {
            &["--api-only", "--shared"]
        } else {
            &["--api-only"]
        };
        let output = generate(root.path(), &lock, &["note", "title:string"], flags);
        if shared && !auth {
            assert!(
                output
                    .unwrap_err()
                    .to_string()
                    .contains("Try: drop --shared")
            );
            continue;
        }
        let up = text(
            &output.unwrap().plan,
            "api/migrations/00001_create_notes.up.sql",
        );
        assert_eq!(
            up.contains("user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE"),
            auth && !shared
        );
        assert_eq!(up.contains("notes_user_id_id_idx"), auth && !shared);
    }
}

#[test]
fn owner_column_conflicts_only_for_owned_resources() {
    let root = tempfile::tempdir().unwrap();
    let mut lock = Lock::new();
    lock.packs.push("auth".into());
    let error = generate(root.path(), &lock, &["note", "user_id:bigint"], &[]).unwrap_err();
    assert!(
        error
            .to_string()
            .contains("conflicts with the resource owner column")
    );
    generate(
        root.path(),
        &lock,
        &["note", "user_id:bigint"],
        &["--shared"],
    )
    .unwrap();
    lock.packs.clear();
    generate(root.path(), &lock, &["note", "user_id:bigint"], &[]).unwrap();
    lock.packs.push("auth".into());
    write(
        root.path(),
        "api/migrations/00001_users.up.sql",
        "CREATE TABLE users (id bigint);",
    );
    let error = generate(root.path(), &lock, &["note", "user:ref:user"], &[]).unwrap_err();
    assert!(error.to_string().contains("Try: choose another field name"));
}

#[test]
fn paths_include_parts_and_exclude_shared_files() {
    let name = Name::parse("line_item").unwrap();
    let paths = paths(&name).unwrap();
    for path in [
        "api/src/handlers/line_items.rs",
        "api/src/handlers/line_items_tests.rs",
        "api/src/models/line_item.rs",
        "web/src/routes/line-items/schema.ts",
    ] {
        assert!(paths.iter().any(|p| p == path), "{paths:?}");
    }
    assert!(paths.windows(2).all(|pair| pair[0] < pair[1]));
    assert!(!paths.iter().any(|p| p.ends_with("mod.rs")
        || p.ends_with("routes_gen.rs")
        || p.contains("migrations")));
}

fn part_output(spec: &Spec<'_>) -> Result<PartPlan> {
    assert_eq!(spec.name.plural_snake(), "todos");
    assert_eq!(spec.fields.len(), 1);
    let mut plan = Plan::new();
    plan.add(file("web/extra"))?;
    Ok(PartPlan {
        plan,
        post_steps: vec![PostStep::Client],
    })
}

fn part(name: &'static str) -> Part {
    Part {
        name,
        plan: part_output,
        paths: None,
    }
}

#[test]
fn parts_sort_and_reject_invalid_registrations() {
    let (z, a) = (part("z"), part("a"));
    assert_eq!(
        sorted_parts([&z, &a])
            .unwrap()
            .iter()
            .map(|p| p.name)
            .collect::<Vec<_>>(),
        ["a", "z"]
    );
    for parts in [vec![part("")], vec![part("same"), part("same")]] {
        let error = sorted_parts(parts.iter()).unwrap_err();
        assert!(error.to_string().contains("\nTry:"));
    }
    assert!(
        registered_parts()
            .unwrap()
            .iter()
            .any(|part| part.name == "pages")
    );
}

#[test]
fn part_runner_merges_outputs_and_skips_with_api_only() {
    let root = tempfile::tempdir().unwrap();
    let lock = Lock::new();
    let fake = Fake::new();
    let cancel = Cancellation::new();
    let values = args(&["todo", "title:string"]);
    let matches = (RESOURCE.args)(clap::Command::new("resource")).get_matches_from(["resource"]);
    let mut input = Input {
        root: root.path(),
        lock: &lock,
        args: &values,
        matches: &matches,
        runner: &fake,
        cancel: &cancel,
    };
    let extra = part("extra");
    let output = build(&input, &[&extra]).unwrap();
    assert!(output.plan.get("web/extra").is_some());
    assert_eq!(
        output.post_steps,
        [
            PostStep::Migrate,
            PostStep::SqlxPrepare,
            PostStep::Build,
            PostStep::OpenApiEmit,
            PostStep::Client
        ]
    );
    assert!(output.plan.get("web/src/lib/nav.gen.ts").is_none());
    let mut conflict = part("conflict");
    conflict.plan = |_| {
        let mut plan = Plan::new();
        plan.add(file("api/migrations/00001_create_todos.up.sql"))?;
        Ok(PartPlan {
            plan,
            ..PartPlan::default()
        })
    };
    let error = build(&input, &[&conflict]).unwrap_err();
    assert!(format!("{error:#}").contains("conflicts"));
    assert!(format!("{error:#}").contains("Try:"));
    let mut failed = part("failed");
    failed.plan = |_| anyhow::bail!("broken input");
    let error = build(&input, &[&failed]).unwrap_err();
    assert!(format!("{error:#}").contains("broken input"));
    assert!(error.to_string().contains("Try:"));
    assert!(
        format!("{error:#}")
            .lines()
            .last()
            .unwrap()
            .starts_with("Try:")
    );
    let matches = (RESOURCE.args)(clap::Command::new("resource"))
        .get_matches_from(["resource", "--api-only"]);
    input.matches = &matches;
    let output = build(&input, &[&failed]).unwrap();
    assert!(output.plan.get("web/extra").is_none());
    assert!(fake.calls().is_empty());
}

#[test]
fn run_registers_resource_and_has_rust_next_steps() {
    let registry = spinup_gen::registry::Registry::builtin().unwrap();
    assert!(registry.lookup("resource").is_some());
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &Lock::new(), &["todo", "title:string"], &[]).unwrap();
    assert_eq!(
        output.next,
        ["spinup db migrate up", "cd api && cargo nextest run"]
    );
    assert_eq!(
        output.post_steps,
        [
            PostStep::Migrate,
            PostStep::SqlxPrepare,
            PostStep::Build,
            PostStep::OpenApiEmit
        ]
    );
    assert!(output.commands.is_empty());
    assert!(
        output
            .plan
            .files
            .iter()
            .filter(|f| f.path.ends_with("mod.rs") || f.path == paths::ROUTES_GEN)
            .all(|f| f.class == "managed")
    );
    assert!(!root.path().join("api").exists());
}

#[test]
fn modules_merge_disk_additions_deletions_and_explicit_removals() {
    let root = tempfile::tempdir().unwrap();
    for path in [
        "api/src/handlers/zebras.rs",
        "api/src/handlers/bees.rs",
        "api/src/handlers/cats.rs",
        "api/src/handlers/zebras_tests.rs",
        "api/src/models/zebra.rs",
        "api/src/models/bee.rs",
        "api/src/handlers/mod.rs",
        "api/src/handlers/README.md",
    ] {
        write(root.path(), path, "existing content");
    }
    let mut plan = Plan::new();
    for path in [
        "api/src/handlers/ants.rs",
        "api/src/models/ant.rs",
        "api/src/handlers/ants_tests.rs",
    ] {
        plan.add(file(path)).unwrap();
    }
    let mut deletion = file("api/src/handlers/bees.rs");
    deletion.delete = true;
    plan.add(deletion).unwrap();
    let removed = args(&["api/src/handlers/cats.rs", "api/src/models/bee.rs"]);
    let planned = modules::plan_module_lists(root.path(), plan, &removed).unwrap();
    assert_eq!(
        text(&planned, paths::HANDLERS_MOD),
        "//! The resource handlers. spinup owns this file.\n\npub mod ants;\npub mod zebras;\n"
    );
    assert_eq!(
        text(&planned, paths::MODELS_MOD),
        "//! The resource models. spinup owns this file.\n\npub mod ant;\npub mod zebra;\n"
    );
    let routes = text(&planned, paths::ROUTES_GEN);
    assert_eq!(
        routes
            .lines()
            .filter(|l| l.contains("router.merge"))
            .collect::<Vec<_>>(),
        [
            "    router = router.merge(handlers::ants::routes());",
            "    router = router.merge(handlers::zebras::routes());",
        ]
    );
    assert_eq!(
        modules::plan_module_lists(root.path(), planned.clone(), &removed).unwrap(),
        planned
    );
    assert_eq!(
        std::fs::read_to_string(root.path().join(paths::HANDLERS_MOD)).unwrap(),
        "existing content"
    );
}

#[test]
fn zero_resource_module_bytes_are_stable() {
    let root = tempfile::tempdir().unwrap();
    let plan = modules::plan_module_lists(root.path(), Plan::new(), &[]).unwrap();
    assert_eq!(
        plan.paths(),
        [paths::HANDLERS_MOD, paths::MODELS_MOD, paths::ROUTES_GEN]
    );
    assert_eq!(
        text(&plan, paths::HANDLERS_MOD),
        "//! The resource handlers. spinup owns this file.\n"
    );
    assert_eq!(
        text(&plan, paths::MODELS_MOD),
        "//! The resource models. spinup owns this file.\n"
    );
    assert_eq!(
        text(&plan, paths::ROUTES_GEN),
        concat!(
            "//! The routes of generated resources. spinup owns this file.\n\n",
            "use kit::db::PgPool;\nuse utoipa_axum::router::OpenApiRouter;\n\n",
            "/// Merges the routes of each resource in name order.\n",
            "pub fn routes() -> OpenApiRouter<PgPool> {\n    OpenApiRouter::new()\n}\n",
        )
    );
}

#[test]
fn readers_report_directory_failures_with_retry_hints() {
    let root = tempfile::tempdir().unwrap();
    write(root.path(), "api/migrations", "file, not directory");
    let error = sql::find_table(root.path(), "todos").unwrap_err();
    assert!(format!("{error:#}").contains("Try: check the directory permissions"));
    assert_eq!(
        spinup_ui::error::parts(&error).try_hint,
        "check the directory permissions"
    );
    write(root.path(), "api/src/handlers", "file, not directory");
    let error = modules::plan_module_lists(root.path(), Plan::new(), &[]).unwrap_err();
    assert!(format!("{error:#}").contains("Try: check the directory permissions"));
    assert_eq!(
        spinup_ui::error::parts(&error).try_hint,
        "check the directory permissions"
    );
}

#[test]
fn pages_plan_client_and_navigation_only_when_enabled() {
    let root = tempfile::tempdir().unwrap();
    let mut lock = Lock::new();
    lock.packs.push("web".into());
    lock.save(root.path().join("spinup.lock")).unwrap();
    for component in [
        "alert-dialog",
        "badge",
        "button",
        "card",
        "checkbox",
        "dropdown-menu",
        "input",
        "label",
        "pagination",
        "table",
    ] {
        write(
            root.path(),
            &format!("web/src/lib/components/ui/{component}/index.ts"),
            "",
        );
    }
    for component in ["field", "page-header", "row-menu"] {
        write(
            root.path(),
            &format!("web/src/lib/components/app/{component}.svelte"),
            "",
        );
    }
    write(
        root.path(),
        "web/package.json",
        r#"{"dependencies":{"sveltekit-superforms":"x","zod":"x","svelte-sonner":"x"}}"#,
    );
    let output = generate(root.path(), &lock, &["todo", "title:string"], &[]).unwrap();
    assert!(
        output
            .plan
            .get("web/src/routes/todos/+page.svelte")
            .is_some()
    );
    assert!(
        text(&output.plan, "web/src/lib/nav.gen.ts").contains("{ href: '/todos', label: 'Todos' }")
    );
    assert_eq!(output.post_steps.last(), Some(&PostStep::Client));
    let output = generate(
        root.path(),
        &lock,
        &["todo", "title:string"],
        &["--api-only"],
    )
    .unwrap();
    assert!(!output.plan.files.iter().any(|f| f.path.starts_with("web/")));
    assert!(!output.post_steps.contains(&PostStep::Client));
    lock.answers.insert(
        "frontend_mode".into(),
        serde_json::value::RawValue::from_string("\"embed\"".into()).unwrap(),
    );
    let error = generate(root.path(), &lock, &["todo", "title:string"], &[]).unwrap_err();
    assert!(format!("{error:#}").contains("cannot generate resource pages in embed mode"));
    generate(
        root.path(),
        &lock,
        &["todo", "title:string"],
        &["--api-only"],
    )
    .unwrap();
}

#[test]
fn invalid_lock_failure_has_retry_hint() {
    let root = tempfile::tempdir().unwrap();
    write(root.path(), "spinup.lock", "invalid");
    let error = generate(root.path(), &Lock::new(), &["todo", "title:string"], &[]).unwrap_err();
    assert!(format!("{error:#}").contains("spinup.lock"));
    assert!(format!("{error:#}").contains("Try:"));
}
