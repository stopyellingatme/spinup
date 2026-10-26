//! The app command `migrate` (Go `kit/db/command.go`).

use std::io::Write;

use super::{DbError, Migrations};
use crate::app::{Command, CommandInput};

/// The usage of the `migrate` command.
pub const MIGRATE_USAGE: &str = "migrate up | down | status | redo | to <version> | version";

/// A parsed subcommand.
enum Sub {
    Up,
    Down,
    Status,
    Redo,
    To(i64),
    Version,
}

fn parse(args: &[String]) -> Result<Sub, DbError> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    let want = match sub {
        "up" | "down" | "status" | "redo" | "version" => 0,
        "to" => 1,
        _ => {
            let why = if sub.is_empty() {
                "no subcommand given".to_owned()
            } else {
                format!("unknown subcommand {sub:?}")
            };
            return Err(DbError::new(
                "migrate",
                format!("{why} (want up, down, status, redo, to, or version)"),
                MIGRATE_USAGE,
            ));
        }
    };
    let rest = &args[1..];
    if rest.len() != want {
        if sub == "to" {
            return Err(DbError::new(
                "migrate to",
                "to needs exactly one version number",
                "migrate to 1",
            ));
        }
        return Err(DbError::new(
            format!("migrate {sub}"),
            format!("unexpected arguments {rest:?}"),
            format!("migrate {sub}"),
        ));
    }
    Ok(match sub {
        "up" => Sub::Up,
        "down" => Sub::Down,
        "status" => Sub::Status,
        "redo" => Sub::Redo,
        "version" => Sub::Version,
        _ => match rest[0].parse::<i64>() {
            Ok(v) if v >= 0 => Sub::To(v),
            _ => {
                return Err(DbError::new(
                    "migrate to",
                    format!(
                        "{:?} is not a migration version (want 0 or a positive number)",
                        rest[0]
                    ),
                    "migrate to 1",
                ));
            }
        },
    })
}

impl Migrations {
    /// Runs the `migrate` command with `args` (the arguments after `migrate`) and writes
    /// its lines to `out`. The output has no durations, so scripts can compare it:
    ///
    /// | Subcommand | Output |
    /// |---|---|
    /// | `up` | `migrate: no pending migrations`, or `migrate: N pending` and `applied  <name>` lines |
    /// | `down` | `rolled back  <name>`, or `migrate: nothing to roll back` |
    /// | `status` | one `applied  <name>` or `pending  <name>` line for each migration, kit migrations first |
    /// | `redo` | `rolled back  <name>` and `applied  <name>`, or `migrate: nothing to redo` |
    /// | `to <v>` | `applied  <name>` or `rolled back  <name>` lines, or `migrate: already at version <v>` |
    /// | `version` | the highest applied app version, 0 when none is applied |
    ///
    /// # Errors
    ///
    /// A usage error with a `Try:` line, or the error of the operation.
    pub async fn run(&self, args: &[String], out: &mut (dyn Write + Send)) -> anyhow::Result<()> {
        match parse(args)? {
            Sub::Up => self.up(out).await,
            Sub::Down => self.down(out).await,
            Sub::Redo => self.redo(out).await,
            Sub::To(v) => self.to(v, out).await,
            Sub::Version => {
                let v = self.version().await?;
                writeln!(out, "{v}")?;
                Ok(())
            }
            Sub::Status => {
                for s in self.status().await? {
                    let state = if s.applied { "applied" } else { "pending" };
                    writeln!(out, "{state}  {}", s.name)?;
                }
                Ok(())
            }
        }
    }
}

/// Returns the app command `migrate` (Go `db.MigrateCommand`). The app registers it with
/// `App::command("migrate", ..)`, so `api migrate <subcommand>` runs the kit migrations and
/// the app migrations. It writes to stdout. See [`Migrations::run`] for the subcommands and
/// their output.
pub fn migrate_command(migrations: Migrations) -> Command {
    Command::new(move |CommandInput { args, .. }| async move {
        let mut out = std::io::stdout();
        migrations.run(&args, &mut out).await
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn err(args: &[&str]) -> String {
        let args: Vec<String> = args.iter().map(|s| (*s).to_owned()).collect();
        match parse(&args) {
            Ok(_) => panic!("migrate {args:?}: no error"),
            Err(e) => e.to_string(),
        }
    }

    #[test]
    fn db_migrate_usage_errors() {
        let cases: [(&[&str], &[&str]); 7] = [
            (&[], &["up, down, status, redo, to, or version", "\nTry: "]),
            (
                &["sideways"],
                &[
                    "\"sideways\"",
                    "up, down, status, redo, to, or version",
                    "\nTry: ",
                ],
            ),
            (&["to"], &["exactly one version", "\nTry: migrate to 1"]),
            (
                &["to", "x1"],
                &["\"x1\" is not a migration version", "\nTry: "],
            ),
            (&["to", "-1"], &["is not a migration version", "\nTry: "]),
            (&["version", "2"], &["unexpected arguments", "\nTry: "]),
            (&["up", "now"], &["unexpected arguments", "\nTry: "]),
        ];
        for (args, want) in cases {
            let msg = err(args);
            for w in want {
                assert!(msg.contains(w), "migrate {args:?}: {msg:?} lacks {w:?}");
            }
        }
    }
}
