use super::*;

/// The verdict for one PostgreSQL major version.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct VersionCheck {
    /// The PostgreSQL major version.
    pub major: u32,
    /// An approaching end-of-life warning, or an empty string.
    pub warn: String,
    /// The reason the version is unsupported, or an empty string.
    pub fail: String,
}

/// Parse `server_version_num` and enforce the Go PostgreSQL version policy.
pub fn check_server_version(num: &str) -> anyhow::Result<VersionCheck> {
    let n = num
        .trim()
        .parse::<u32>()
        .ok()
        .filter(|n| *n >= 10000)
        .ok_or_else(|| {
            Error::new("Database check failed")
                .why(format!("{:?} is not a server_version_num", num.trim()))
                .try_hint("check the PostgreSQL server version")
        })?;
    let major = n / 10000;
    Ok(VersionCheck {
        major,
        warn: if major == 14 {
            "PostgreSQL 14 reaches its end of life on 2026-11-12".into()
        } else {
            String::new()
        },
        fail: if major < 14 {
            format!("PostgreSQL {major} is past its end of life and is not supported")
        } else if major > 18 {
            format!("PostgreSQL {major} is a beta and is not allowed")
        } else {
            String::new()
        },
    })
}

/// Check the local PostgreSQL version and report pending kit and app migrations.
pub fn doctor(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
) -> anyhow::Result<()> {
    let what = "Database check failed";
    let (root, config) = load(o, what)?;
    guard_local(o, &config, what, "db doctor", false)?;
    let cmd = compose(
        &root,
        false,
        &[
            "psql",
            "-U",
            "app",
            "-d",
            "app",
            "-At",
            "-c",
            "SELECT current_setting('server_version_num')",
        ],
    );
    if o.dry_run {
        print_cmd(o, what, &cmd, "")?;
        return status(o, runner, cancel);
    }
    let output = capture(o, runner, cancel, what, &cmd, false)?;
    let check = check_server_version(&String::from_utf8_lossy(&output.stdout))?;
    if check.fail.is_empty() {
        write_out(o, what, &format!("ok  PostgreSQL {}\n", check.major))?;
    }
    if !check.warn.is_empty() {
        write_err(
            o,
            what,
            &format!(
                "Warning: {}.\n  Try: upgrade to PostgreSQL 18 before that date\n",
                check.warn
            ),
        )?;
    }
    let mut buffer = Vec::new();
    let err_out = o
        .err_out
        .as_mut()
        .map(|err| &mut **err as &mut (dyn Write + Send));
    status(
        &mut Options {
            dir: root,
            env: o.env.clone(),
            lookup: o.lookup,
            out: Some(&mut buffer),
            err_out,
            ..Options::default()
        },
        runner,
        cancel,
    )?;
    let text = String::from_utf8_lossy(&buffer);
    let pending: Vec<_> = text
        .lines()
        .filter_map(|line| {
            let (version, rest) = line.trim().split_once('/')?;
            let name = rest.strip_prefix("pending ")?;
            Some(format!("{version} {name}"))
        })
        .collect();
    if pending.is_empty() {
        write_out(o, what, "ok  no pending migrations\n")?;
    } else {
        write_err(
            o,
            what,
            &format!(
                "Warning: {} pending migrations: {}.\n  Try: spinup db migrate up\n",
                pending.len(),
                pending.join(", ")
            ),
        )?;
    }
    if !check.fail.is_empty() {
        return Err(Error::new(what)
            .why(check.fail)
            .try_hint(format!(
                "run PostgreSQL 15 to 18 (the postgres image in {COMPOSE_FILE})"
            ))
            .into());
    }
    Ok(())
}
