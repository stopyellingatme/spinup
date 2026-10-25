//! The three-way merge contract: [`Input`], [`Merged`], [`Merger`], and [`MAX_SIZE`] (Go
//! `internal/merge/contract.go`, ADR 0020 §1).
//!
//! Contract file from the R1 pre-seed. Main loop only: units do not edit it.
//!
//! Unit R1.8 implements [`Merger`] with `git merge-file` through `spinup-exec`.

use spinup_exec::runner::Cancellation;

/// The largest file that an upgrade merges, in bytes. The planner overwrites or keeps a
/// larger file, and never sends one to a [`Merger`].
pub const MAX_SIZE: usize = 1 << 20;

/// One three-way merge. The caller normalizes all three inputs (LF line endings, one
/// trailing newline) and never passes binary data.
#[derive(Debug, Clone, Copy)]
pub struct Input<'a> {
    /// The slash-separated project path, for labels and messages.
    pub path: &'a str,
    /// The OLD render. It is empty when the file is new in the template.
    pub base: &'a [u8],
    /// The file on disk (the user's version).
    pub ours: &'a [u8],
    /// The NEW render.
    pub theirs: &'a [u8],
}

/// A merged file (Go `merge.Result`).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Merged {
    /// The merged content. It holds diff3 conflict markers when `conflicted` is true.
    pub data: Vec<u8>,
    /// True when at least one hunk did not merge.
    pub conflicted: bool,
}

/// Merges text three ways.
pub trait Merger: Send + Sync {
    /// Merges `input`. A conflict is not an error: it returns `conflicted: true`.
    fn merge(&self, input: &Input<'_>, cancel: &Cancellation) -> anyhow::Result<Merged>;
}
