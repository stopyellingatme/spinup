use super::*;

#[test]
fn unsafe_sql_and_safe_sql_follow_go_rules() {
    for (sql, rule) in [
        ("DROP TABLE todos;", "drop-table"),
        ("ALTER TABLE todos DROP old;", "drop-column"),
        (
            "ALTER TABLE todos ALTER title SET DATA TYPE bigint;",
            "alter-type",
        ),
        (
            "ALTER TABLE todos ALTER title SET NOT NULL;",
            "set-not-null",
        ),
        (
            "ALTER TABLE todos ADD a int DEFAULT 1, ADD b int NOT NULL;",
            "set-not-null",
        ),
        ("ALTER TABLE todos RENAME TO tasks;", "rename"),
        (
            "DO $$ BEGIN DROP TABLE todos; END $$;",
            "unchecked-dynamic-sql",
        ),
        (
            "CREATE INDEX CONCURRENTLY idx ON todos (id);",
            "concurrent-in-tx",
        ),
        ("CREATE INDEX idx ON todos (id);", "index-not-concurrent"),
    ] {
        let got = lint("test.up.sql", sql.as_bytes()).unwrap();
        assert_eq!(got.len(), 1, "{sql}: {got:?}");
        assert_eq!(got[0].rule, rule);
        assert_eq!(got[0].line, 1);
        assert!(!got[0].try_hint.is_empty());
    }
    for sql in [
        "SELECT 'DROP TABLE ignored'; SELECT $$ALTER TABLE ignored DROP x$$;",
        "ALTER TABLE todos ADD title text NOT NULL DEFAULT '';",
        "ALTER TABLE todos DROP CONSTRAINT title_key;",
        "ALTER TABLE todos DROP NOT NULL;",
        "ALTER TABLE todos ADD code int GENERATED ALWAYS AS (id) STORED NOT NULL;",
        "CREATE TABLE todos (id int); CREATE INDEX idx ON todos (id);",
        "-- no-transaction\nCREATE INDEX CONCURRENTLY idx ON todos (id);",
        "-- spinup:allow-destructive archived data\nDROP TABLE todos;",
    ] {
        assert!(
            lint("test.up.sql", sql.as_bytes()).unwrap().is_empty(),
            "{sql}"
        );
    }
}

#[test]
fn directory_lint_uses_history_and_ignores_down_files() {
    let dir = tempfile::tempdir().unwrap();
    for (name, body) in [
        (
            "00001_init.up.sql",
            "CREATE UNLOGGED TABLE public.todos(id int);",
        ),
        ("00001_init.down.sql", "DROP TABLE todos;"),
        (
            "00002_index.up.sql",
            "CREATE UNIQUE INDEX idx ON ONLY todos(id);",
        ),
    ] {
        std::fs::write(dir.path().join(name), body).unwrap();
    }
    let got = lint_dir(dir.path()).unwrap();
    assert_eq!(got.len(), 1);
    assert_eq!(got[0].file, "00002_index.up.sql");
    assert_eq!(got[0].rule, "index-not-concurrent");
}

#[test]
fn overrides_are_adjacent_and_need_reasons() {
    let got = lint("a.up.sql", b"-- spinup:allow-destructive\nDROP TABLE a;").unwrap();
    assert_eq!(got.len(), 2);
    assert_eq!(got[0].rule, "bad-override");
    let got = lint(
        "a.up.sql",
        b"-- spinup:allow-destructive reason\n\nDROP TABLE a;",
    )
    .unwrap();
    assert_eq!(got[0].line, 3);
}

#[test]
fn transaction_directive_must_be_at_start_as_sqlx_requires() {
    for sql in [
        "\n-- no-transaction\nCREATE INDEX CONCURRENTLY idx ON todos(id);",
        "SELECT 1;\n-- no-transaction\nCREATE INDEX CONCURRENTLY idx ON todos(id);",
    ] {
        let got = lint("a.up.sql", sql.as_bytes()).unwrap();
        assert_eq!(got.len(), 1, "{sql}");
        assert_eq!(got[0].rule, "concurrent-in-tx");
    }
}

#[test]
fn selected_files_and_command_output_are_sorted_and_keep_json_fields() {
    let dir = tempfile::tempdir().unwrap();
    std::fs::create_dir_all(dir.path().join("api/migrations")).unwrap();
    std::fs::write(dir.path().join("spinup.toml"), "[app]\nname='todo'\n").unwrap();
    for (name, body) in [
        ("b.up.sql", "\nDROP TABLE b;"),
        ("a.up.sql", "DROP TABLE a;"),
        ("a.down.sql", "DROP TABLE ignored;"),
    ] {
        std::fs::write(dir.path().join(name), body).unwrap();
    }
    let files = vec!["b.up.sql".into(), "a.down.sql".into(), "a.up.sql".into()];
    let got = lint_files(dir.path(), &files).unwrap();
    assert_eq!(got.len(), 2);
    assert_eq!(got[0].file, "a.up.sql");
    assert_eq!(got[1].line, 2);
    let mut out = Vec::new();
    let error = run(dir.path(), &files, true, &mut out).unwrap_err();
    let value: serde_json::Value = serde_json::from_slice(&out).unwrap();
    assert_eq!(value[0]["rule"], "drop-table");
    assert!(value[0]["try"].is_string());
    assert!(value[0].get("try_hint").is_none());
    assert_eq!(spinup_ui::error::parts(&error).what, "Database lint failed");
    let mut out = Vec::new();
    run(dir.path(), &files, false, &mut out).unwrap_err();
    assert!(
        String::from_utf8(out)
            .unwrap()
            .starts_with("a.up.sql:1: drop-table:")
    );
    assert!(lint_files(dir.path(), &["missing.up.sql".into()]).is_err());
    assert!(lint_dir(&dir.path().join("missing")).is_err());
    let mut out = Vec::new();
    run(dir.path(), &[], true, &mut out).unwrap();
    assert_eq!(out, b"[]\n");
}

#[test]
fn quoted_tables_nested_comments_and_string_bodies_are_safe() {
    for table in ["public.todos", "\"public\".\"todos\"", "public.\"todos\""] {
        let sql = format!("CREATE TABLE {table}(id int); CREATE INDEX idx ON todos(id);");
        assert!(
            lint("a.up.sql", sql.as_bytes()).unwrap().is_empty(),
            "{sql}"
        );
    }
    for sql in [
        "/* outer /* inner */ DROP TABLE ignored; */ SELECT 1;",
        "SELECT 'it''s safe; DROP TABLE ignored'; SELECT $tag$\nDROP TABLE ignored;\n$tag$;",
        "CREATE FUNCTION example() RETURNS void AS $$ BEGIN DROP TABLE ignored; END; $$ LANGUAGE plpgsql;",
    ] {
        assert!(lint("a.up.sql", sql.as_bytes()).unwrap().is_empty());
    }
    let got = lint("a.up.sql", b"/* first\nsecond */\nALTER TABLE todos ADD COLUMN x numeric(4, 2) DEFAULT 1, DROP COLUMN old;").unwrap();
    assert_eq!(got[0].line, 3);
    assert_eq!(got[0].rule, "drop-column");
}
