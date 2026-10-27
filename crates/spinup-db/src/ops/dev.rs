use super::*;
use crate::names::{SEEDS_DIR, sql_files};
use spinup_exec::runner::Stdin;

/// Open psql on the local development database with the process terminal.
pub fn psql(o: &mut Options<'_>, runner: &dyn Runner, cancel: &Cancellation) -> anyhow::Result<()> {
    let what = "Cannot open psql";
    let (root, config) = load(o, what)?;
    guard_local(o, &config, what, "db psql", false)?;
    let mut cmd = compose(&root, true, &["psql", "-U", "app", "-d", "app"]);
    cmd.interactive = true;
    cmd.stdin = Stdin::Inherit;
    stream(o, runner, cancel, what, &cmd)
}

/// Stream a custom-format database archive to a temporary file and replace the destination on success.
/// An empty file path selects [`DEFAULT_DUMP_FILE`] under the project root.
pub fn dump(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
    file: &str,
) -> anyhow::Result<()> {
    let what = "Cannot dump the database";
    let (root, config) = load(o, what)?;
    guard_local(o, &config, what, "db dump", false)?;
    let shown = if file.is_empty() {
        DEFAULT_DUMP_FILE
    } else {
        file
    };
    let path = if file.is_empty() {
        root.join(DEFAULT_DUMP_FILE)
    } else {
        PathBuf::from(file)
    };
    let cmd = compose(&root, false, &["pg_dump", "-U", "app", "-d", "app", "-Fc"]);
    if o.dry_run {
        return print_cmd(o, what, &cmd, &format!("> {}", Cmd::new(shown)));
    }
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    std::fs::create_dir_all(parent).map_err(|e| op_error(what, e))?;
    let mut temp = tempfile::NamedTempFile::new_in(parent).map_err(|e| op_error(what, e))?;
    let err = o
        .err_out
        .as_mut()
        .map(|err| &mut **err as &mut (dyn Write + Send));
    runner
        .run(
            &cmd,
            cancel,
            Sinks {
                stdout: Some(temp.as_file_mut()),
                stderr: err,
            },
        )
        .map_err(|e| op_error(what, e))?;
    temp.as_file_mut().flush().map_err(|e| op_error(what, e))?;
    temp.persist(&path).map_err(|e| op_error(what, e.error))?;
    write_out(o, what, &format!("Wrote {shown}\n"))
}

/// Restore a custom-format archive after local and confirmation guards.
pub fn restore(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
    file: &str,
) -> anyhow::Result<()> {
    let what = "Cannot restore the database";
    let (root, config) = load(o, what)?;
    guard_local(o, &config, what, "db restore", true)?;
    if !Path::new(file).is_file() {
        return Err(Error::new(what)
            .why(format!("{file} is not a dump file"))
            .try_hint(format!(
                "spinup db dump, then spinup db restore {DEFAULT_DUMP_FILE}"
            ))
            .into());
    }
    confirm_yes(
        o,
        what,
        &format!("Replace the objects in the development database with {file}?"),
        &format!("spinup db restore {file} --yes"),
    )?;
    let mut cmd = compose(
        &root,
        false,
        &[
            "pg_restore",
            "-U",
            "app",
            "-d",
            "app",
            "--clean",
            "--if-exists",
        ],
    );
    if o.dry_run {
        return print_cmd(o, what, &cmd, &format!("< {}", Cmd::new(file)));
    }
    cmd.stdin = Stdin::Bytes(std::fs::read(file).map_err(|e| op_error(what, e))?);
    stream(o, runner, cancel, what, &cmd)?;
    write_out(o, what, &format!("Restored {file}\n"))
}

/// Apply local SQL seed files in filename order with `ON_ERROR_STOP=1`.
pub fn seed(o: &mut Options<'_>, runner: &dyn Runner, cancel: &Cancellation) -> anyhow::Result<()> {
    let what = "Cannot seed the database";
    let (root, config) = load(o, what)?;
    guard_local(o, &config, what, "db seed", true)?;
    let files = sql_files(&root.join(SEEDS_DIR)).map_err(|e| op_error(what, e))?;
    if files.is_empty() {
        return write_out(
            o,
            what,
            &format!(
                "No seed files in {SEEDS_DIR}.\nTry: add a file such as {SEEDS_DIR}/01_users.sql with INSERT statements\n"
            ),
        );
    }
    for file in files {
        let relative = format!("{SEEDS_DIR}/{file}");
        let mut cmd = compose(
            &root,
            false,
            &[
                "psql",
                "-U",
                "app",
                "-d",
                "app",
                "-v",
                "ON_ERROR_STOP=1",
                "-f",
                "-",
            ],
        );
        if o.dry_run {
            print_cmd(o, what, &cmd, &format!("< {}", Cmd::new(&relative)))?;
            continue;
        }
        write_out(o, what, &format!("seed  {relative}\n"))?;
        cmd.stdin =
            Stdin::Bytes(std::fs::read(root.join(&relative)).map_err(|e| op_error(what, e))?);
        stream(o, runner, cancel, what, &cmd)?;
    }
    Ok(())
}

/// Drop and recreate the local public schema, then apply kit and app migrations.
/// Require `--yes` or a terminal confirmation, plus any protected environment confirmation.
pub fn reset(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
) -> anyhow::Result<()> {
    let what = "Cannot reset the database";
    let (root, config) = load(o, what)?;
    guard_local(o, &config, what, "db reset", true)?;
    confirm_yes(
        o,
        what,
        "Drop every table in the development database and migrate up?",
        "spinup db reset --yes",
    )?;
    // Resolve the URL before destructive work. A missing URL must not leave an empty schema.
    let _ = database_cmd(o, &root, Cmd::new("sqlx"), what)?;
    let cmd = compose(
        &root,
        false,
        &[
            "psql",
            "-U",
            "app",
            "-d",
            "app",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;",
        ],
    );
    stream(o, runner, cancel, what, &cmd)?;
    crate::migrate::run_at(o, &root, crate::migrate::Sub::Up, runner, cancel)
}
