//! Git worktrees and commits for an isolated upgrade.

use std::path::{Path, PathBuf};

use anyhow::{Result, anyhow, bail};
use spinup_exec::runner::{Cancellation, Cmd, Runner};

/// Git operations that use the shared subprocess runner.
pub struct Git<'a> {
    /// Runs each Git command.
    pub runner: &'a dyn Runner,
    /// Stops each Git command when cancelled.
    pub cancel: &'a Cancellation,
}

impl Git<'_> {
    /// Runs Git and returns its standard output.
    pub fn run(&self, dir: &Path, args: &[&str]) -> Result<String> {
        let output = self.runner.output(&Cmd::new("git").args(args.iter().copied()).dir(dir), self.cancel)
            .map_err(|e| anyhow!("git {} failed: {e}\nTry: correct the Git error, then run spinup upgrade --continue or --abort", args.join(" ")))?;
        Ok(String::from_utf8_lossy(&output.stdout).into_owned())
    }

    /// Requires the project root to be the Git root.
    pub fn check_repository(&self, root: &Path) -> Result<()> {
        let top = self.run(root, &["rev-parse", "--show-toplevel"])?;
        if std::fs::canonicalize(top.trim()).ok().as_deref() != Some(root) {
            bail!(
                "upgrade repository {root:?}: Git root is {:?}\nTry: run spinup upgrade at the project Git root",
                top.trim()
            );
        }
        Ok(())
    }

    /// Requires local state to be ignored. Only plugin trust files may be tracked.
    pub fn check_ignored(&self, root: &Path) -> Result<()> {
        if self
            .run(
                root,
                &["check-ignore", "-q", "--no-index", ".spinup/upgrade.json"],
            )
            .is_err()
        {
            bail!(
                "check upgrade storage: local .spinup/ state must be ignored by Git\nTry: ignore .spinup/* except plugins.allow and plugins.history, then commit .gitignore"
            );
        }
        for name in self
            .run(root, &["ls-files", "-z", "--", ".spinup"])?
            .split('\0')
            .filter(|s| !s.is_empty())
        {
            if name != ".spinup/plugins.allow" && name != ".spinup/plugins.history" {
                bail!(
                    "check upgrade storage: .spinup/ contains tracked state {name:?}\nTry: remove local state from the Git index and commit the change"
                );
            }
        }
        if self
            .run(
                root,
                &["check-ignore", "-q", "--no-index", ".spinup/upgrade/probe"],
            )
            .is_err()
        {
            bail!(
                "check upgrade storage: .spinup/upgrade must be ignored by Git\nTry: ignore .spinup/* except plugins.allow and plugins.history, then commit .gitignore"
            );
        }
        Ok(())
    }

    /// Creates a branch and checks the ignore policy in both checkouts.
    pub fn create(&self, root: &Path, to: &str, branch: &str) -> Result<PathBuf> {
        crate::chain::parse(to)?;
        let work = root.join(".spinup/upgrade").join(to);
        self.check_ignored(root)?;
        self.run(
            root,
            &["worktree", "add", "-b", branch, text(&work)?, "HEAD"],
        )?;
        if let Err(error) = self.check_ignored(&work) {
            self.run(root, &["worktree", "remove", "--force", text(&work)?])?;
            self.run(root, &["branch", "-D", branch])?;
            return Err(error);
        }
        Ok(work)
    }

    /// Validates the recorded worktree before recovery or removal.
    pub fn check(&self, work: &Path, branch: &str) -> Result<()> {
        let top = self.run(work, &["rev-parse", "--show-toplevel"])?;
        let resolved = std::fs::canonicalize(work).map_err(|e| {
            anyhow!("resume upgrade: cannot resolve the worktree: {e}\nTry: restore the upgrade worktree at {}", work.display())
        })?;
        if std::fs::canonicalize(top.trim()).ok().as_deref() != Some(resolved.as_path())
            || resolved != work
        {
            bail!(
                "resume upgrade: the worktree path does not match its Git root\nTry: restore the upgrade worktree at {}",
                work.display()
            );
        }
        if self.run(work, &["symbolic-ref", "--short", "HEAD"])?.trim() != branch {
            bail!(
                "resume upgrade: worktree is on an unexpected branch\nTry: restore branch {branch} in {}",
                work.display()
            );
        }
        self.check_ignored(work)
    }

    /// Stages all changes and makes a commit. `always` permits an empty commit.
    pub fn commit(&self, work: &Path, message: &str, always: bool) -> Result<()> {
        self.run(work, &["add", "-A", "--", "."])?;
        if !always
            && self
                .run(work, &["diff", "--cached", "--name-only"])?
                .is_empty()
        {
            return Ok(());
        }
        self.run(work, &["commit", "--allow-empty", "-m", message])?;
        Ok(())
    }

    /// Removes the recorded worktree and branch. Missing worktrees are pruned.
    pub fn remove(&self, root: &Path, work: &Path, branch: &str) -> Result<()> {
        match std::fs::symlink_metadata(work) {
            Ok(_) => {
                self.check(work, branch)?;
                self.run(root, &["worktree", "remove", "--force", text(work)?])?;
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                self.run(root, &["worktree", "prune"])?;
            }
            Err(e) => bail!("inspect upgrade worktree: {e}\nTry: check the worktree permissions"),
        }
        if !self
            .run(root, &["branch", "--list", branch])?
            .trim()
            .is_empty()
        {
            self.run(root, &["branch", "-D", branch])?;
        }
        Ok(())
    }
}

fn text(path: &Path) -> Result<&str> {
    path.to_str().ok_or_else(|| {
        anyhow!("use upgrade worktree {path:?}: path is not UTF-8\nTry: use a UTF-8 project path")
    })
}
