//! Refresh the SQLx query cache after applying migrations.

use crate::ops::{Options, database_cmd, load, stream};
use spinup_exec::runner::{Cancellation, Cmd, Runner};

/// Apply kit and app migrations, then cache queries from all workspace targets.
/// Force online query checking in the child even when the parent uses offline SQLx.
pub fn run(o: &mut Options<'_>, runner: &dyn Runner, cancel: &Cancellation) -> anyhow::Result<()> {
    crate::migrate::up(o, runner, cancel)?;
    let what = "Cannot prepare the query cache";
    let (root, _) = load(o, what)?;
    let cmd = Cmd::new("cargo")
        .args(["sqlx", "prepare", "--workspace", "--", "--all-targets"])
        .dir(root.join("api"))
        .env("SQLX_OFFLINE", "false");
    let cmd = database_cmd(o, &root, cmd, what)?;
    stream(o, runner, cancel, what, &cmd)
}
