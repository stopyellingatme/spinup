//! The generator contract: [`PostStep`], [`Input`], [`Output`], [`Command`], [`Generator`],
//! and the [`GENERATORS`] slice (Go `internal/gen/contract.go`, ADR 0007, ADR 0020 §1 and §5).
//!
//! Contract file from the R1 pre-seed. Main loop only: units do not edit it.
//!
//! Generators never write to disk. They return a plan, and the runner (unit R1.10) applies
//! it through `spinup-vfs`. The `clippy.toml` of this crate bans the file-write functions.
//!
//! A generator registers itself in its own module:
//!
//! ```ignore
//! #[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
//! static RESOURCE: Generator = Generator { name: "resource", /* ... */ };
//! ```

use std::path::Path;

use spinup_exec::runner::{Cancellation, Runner};
use spinup_manifest::lock::Lock;
use spinup_vfs::plan::Plan;

/// A tool run after the files of a generator are written. The runner runs the requested
/// steps in the order of the variants (ADR 0019 §4). `rustfmt` is not a post-step: it runs
/// in the plan step, before apply, and the lock hash covers the formatted bytes.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum PostStep {
    /// `migrate up` on the development database.
    Migrate,
    /// `cargo sqlx prepare` (refreshes `.sqlx/`).
    SqlxPrepare,
    /// Build the API.
    Build,
    /// Emit the OpenAPI bundle from the API (`api openapi`).
    OpenApiEmit,
    /// Regenerate the web API client from the OpenAPI bundle.
    Client,
}

/// What a generator reads. Generators never write to disk.
#[derive(Clone, Copy)]
pub struct Input<'a> {
    /// The project root (the directory that holds `spinup.toml`).
    pub root: &'a Path,
    /// The project lock. Generators read it (answers, tracked files) and never change it.
    pub lock: &'a Lock,
    /// The positional arguments after the generator name.
    pub args: &'a [String],
    /// The parsed arguments of the generator subcommand, including its own flags.
    pub matches: &'a clap::ArgMatches,
    /// Runs the read-only tools of the plan step, such as `promtool check rules`
    /// (ADR 0021 §7). A generator never runs a tool that writes the project. The runner,
    /// not the generator, formats the planned Rust files.
    pub runner: &'a dyn Runner,
    /// The cancel signal of the CLI.
    pub cancel: &'a Cancellation,
}

impl std::fmt::Debug for Input<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Input")
            .field("root", &self.root)
            .field("args", &self.args)
            .finish_non_exhaustive()
    }
}

/// One external command that a generator asks the runner to run after the write and
/// before the post-steps (ADR 0008). `--dry-run` prints it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Command {
    /// The working directory, relative to the project root.
    pub dir: String,
    /// The program and its arguments.
    pub argv: Vec<String>,
}

/// What a generator returns.
#[derive(Debug, Clone, Default)]
pub struct Output {
    /// The files to write. Each file has a class.
    pub plan: Plan,
    /// The post-steps to run after the write. The runner sorts them in [`PostStep`] order.
    pub post_steps: Vec<PostStep>,
    /// Up to two commands to suggest after the run.
    pub next: Vec<String>,
    /// External generator commands, such as `pnpm dlx shadcn-svelte@<v> add button`.
    pub commands: Vec<Command>,
    /// The plan paths that the generator built from the current content on disk (an
    /// in-place edit that keeps the user's content). The runner updates them without
    /// `--force` and keeps their lock template.
    pub edits: Vec<String>,
}

/// One `spinup generate <name>` kind.
pub struct Generator {
    /// The kind, such as `resource`.
    pub name: &'static str,
    /// Other names for the kind.
    pub aliases: &'static [&'static str],
    /// One help line.
    pub short: &'static str,
    /// The argument synopsis, such as `<name> <field:type>...`.
    pub usage: &'static str,
    /// Adds the generator's own arguments and flags to its subcommand.
    pub args: fn(clap::Command) -> clap::Command,
    /// Plans the files. It must be deterministic for the same input.
    pub run: fn(&Input<'_>) -> anyhow::Result<Output>,
}

impl std::fmt::Debug for Generator {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Generator")
            .field("name", &self.name)
            .field("aliases", &self.aliases)
            .finish_non_exhaustive()
    }
}

/// Every built-in generator, in link order. The registry (unit R1.10) sorts them by name
/// and adds the declarative generators of plugins.
#[linkme::distributed_slice]
pub static GENERATORS: [Generator];
