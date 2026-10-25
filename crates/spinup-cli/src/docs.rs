//! `spinup docs compat` and `spinup docs reference`, and the docs site pages (Go
//! `internal/cli/docs.go` and `internal/docsite`).
//!
//! The Rust site is `docs/site-rs/` until R5 (ADR 0020 §8). The test `site_is_current`
//! compares it with the pages of the command tree. `SPINUP_GOLDEN_UPDATE=1` rewrites it.

use std::collections::BTreeMap;
use std::path::{Component, Path, PathBuf};

use anyhow::Result;
use clap::{Arg, ArgAction, ArgMatches, Command};
use spinup_stack::stack::Stack;
use spinup_tplstore::source::Source;
use spinup_tplstore::store;
use spinup_ui::error::Error as UiError;
use spinup_ui::exit;
use spinup_vfs::apply::{ApplyOptions, apply};
use spinup_vfs::plan::{File, Plan};

use crate::root::{App, COMMANDS, CommandDef, Group};
use crate::version::VERSION;

/// The behavior versions that this CLI supports.
pub const BEHAVIOR_VERSIONS: [u32; 1] = [1];

/// The oldest PostgreSQL major that spinup allows (Go `db/ops` `pgOldestAllowed`).
const PG_OLDEST_ALLOWED: u32 = 14;
/// The newest PostgreSQL major that spinup allows (Go `db/ops` `pgNewestAllowed`).
const PG_NEWEST_ALLOWED: u32 = 18;

/// The versions that work together (Go `docsite.Compatibility`).
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Compatibility {
    /// The CLI version.
    pub cli_version: String,
    /// The rule for the kit version.
    pub kit_version_rule: String,
    /// The supported template versions, newest first.
    pub template_versions: Vec<String>,
    /// The supported behavior versions.
    pub behavior_versions: Vec<u32>,
    /// The supported PostgreSQL majors.
    pub postgresql_majors: Vec<u32>,
    /// The Go toolchain and the Go minimum of generated apps.
    pub go: String,
    /// The Node image tag.
    pub node: String,
    /// The SvelteKit version.
    pub sveltekit: String,
    /// The pinned container images, sorted by name.
    pub images: Vec<Image>,
}

/// One immutable container image pin.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Image {
    /// The pin name in the stack versions file.
    pub name: String,
    /// The reference `<image>:<tag>@<digest>`.
    #[serde(rename = "ref")]
    pub reference: String,
}

impl Compatibility {
    /// Builds the matrix from the template versions and the pinned stack (Go
    /// `docsite.NewCompatibility`).
    pub fn new(cli_version: &str, template_versions: &[String], stack: &Stack) -> Self {
        Self {
            cli_version: cli_version.into(),
            kit_version_rule: "CLI and kit major and minor versions must match.".into(),
            template_versions: spinup_compat::check::supported_template_versions(
                cli_version,
                template_versions,
            ),
            behavior_versions: BEHAVIOR_VERSIONS.to_vec(),
            postgresql_majors: (PG_OLDEST_ALLOWED..=PG_NEWEST_ALLOWED).collect(),
            go: format!(
                "{} (generated apps require Go {})",
                stack.go.toolchain, stack.go.min
            ),
            node: stack
                .images
                .get("node")
                .map(|image| image.tag.trim_start_matches('v').to_string())
                .unwrap_or_default(),
            sveltekit: stack.npm.get("@sveltejs/kit").cloned().unwrap_or_default(),
            images: stack
                .images
                .iter()
                .map(|(name, image)| Image {
                    name: name.clone(),
                    reference: image.reference(),
                })
                .collect(),
        }
    }

    /// The matrix as Markdown.
    pub fn markdown(&self) -> String {
        let mut out = String::from("# Compatibility\n\n");
        out.push_str(&format!("- CLI version: `{}`\n", self.cli_version));
        out.push_str(&format!("- Kit rule: {}\n", self.kit_version_rule));
        out.push_str(&format!(
            "- Template versions: {}\n",
            markdown_list(&self.template_versions)
        ));
        out.push_str(&format!(
            "- Behavior versions: {}\n",
            int_list(&self.behavior_versions)
        ));
        out.push_str(&format!(
            "- PostgreSQL majors: {}\n",
            int_list(&self.postgresql_majors)
        ));
        out.push_str(&format!("- Go: {}\n", self.go));
        out.push_str(&format!("- Node: {}\n", self.node));
        out.push_str(&format!("- SvelteKit: {}\n\n", self.sveltekit));
        out.push_str("## Pinned images\n\n| Name | Image |\n| --- | --- |\n");
        for image in &self.images {
            out.push_str(&format!("| `{}` | `{}` |\n", image.name, image.reference));
        }
        out
    }
}

fn markdown_list(values: &[String]) -> String {
    if values.is_empty() {
        return "none".into();
    }
    values
        .iter()
        .map(|value| format!("`{value}`"))
        .collect::<Vec<_>>()
        .join(", ")
}

fn int_list(values: &[u32]) -> String {
    values
        .iter()
        .map(ToString::to_string)
        .collect::<Vec<_>>()
        .join(", ")
}

/// The full path of a command, such as `spinup cache list`.
fn full_path(parents: &[&str], command: &Command) -> String {
    let mut parts: Vec<&str> = parents.to_vec();
    parts.push(command.get_name());
    parts.join(" ")
}

/// The page path of a command, such as `commands/cache-list.md`.
fn page_path(path: &str) -> String {
    let parts: Vec<&str> = path.split(' ').skip(1).collect();
    format!("commands/{}.md", parts.join("-"))
}

/// Reports whether the docs leave out the command (`help`, `completion`, hidden commands).
fn skipped(command: &Command) -> bool {
    command.is_hide_set() || matches!(command.get_name(), "help" | "completion")
}

/// One visible command with its full path and the global flags of the root.
struct Entry<'a> {
    path: String,
    command: &'a Command,
}

fn command_tree<'a>(root: &'a Command) -> Vec<Entry<'a>> {
    fn visit<'a>(parents: &mut Vec<&'a str>, command: &'a Command, out: &mut Vec<Entry<'a>>) {
        for child in command.get_subcommands() {
            if skipped(child) {
                continue;
            }
            out.push(Entry {
                path: full_path(parents, child),
                command: child,
            });
            parents.push(child.get_name());
            visit(parents, child, out);
            parents.pop();
        }
    }
    let mut out = Vec::new();
    visit(&mut vec![root.get_name()], root, &mut out);
    out.sort_by(|a, b| a.path.cmp(&b.path));
    out
}

fn flag_row(arg: &Arg) -> Option<(String, String, String, String)> {
    let long = arg.get_long()?;
    if matches!(arg.get_id().as_str(), "help" | "version") {
        return None;
    }
    let mut name = format!("--{long}");
    if let Some(short) = arg.get_short() {
        name = format!("-{short}, {name}");
    }
    let default = match arg.get_action() {
        ArgAction::SetTrue => "false".to_string(),
        ArgAction::SetFalse => "true".to_string(),
        _ => arg
            .get_default_values()
            .iter()
            .map(|value| value.to_string_lossy().into_owned())
            .collect::<Vec<_>>()
            .join(","),
    };
    let help = arg.get_help().map(ToString::to_string).unwrap_or_default();
    Some((long.to_string(), name, default, help.replace('|', "\\|")))
}

/// The use line of a command: the path, the positional arguments, and `[flags]` when the
/// command has its own flags.
fn use_line(path: &str, command: &Command) -> String {
    let mut line = path.to_string();
    for arg in command.get_positionals() {
        let name = arg
            .get_value_names()
            .and_then(|names| names.first())
            .map_or_else(|| arg.get_id().to_string(), ToString::to_string);
        let many = arg
            .get_num_args()
            .is_some_and(|range| range.max_values() > 1)
            || matches!(arg.get_action(), ArgAction::Append);
        let dots = if many { "..." } else { "" };
        if arg.is_required_set() {
            line.push_str(&format!(" <{name}{dots}>"));
        } else {
            line.push_str(&format!(" [{name}{dots}]"));
        }
    }
    if command
        .get_arguments()
        .any(|arg| !arg.is_positional() && !arg.is_global_set() && flag_row(arg).is_some())
    {
        line.push_str(" [flags]");
    }
    line
}

fn render_command(path: &str, command: &Command, globals: &[&Arg]) -> String {
    let about = command
        .get_about()
        .map(ToString::to_string)
        .unwrap_or_default();
    let mut out = format!("# {path}\n\n{about}\n\n");
    if let Some(long) = command.get_long_about().map(ToString::to_string)
        && long != about
    {
        out.push_str(&format!("{long}\n\n"));
    }
    out.push_str(&format!(
        "## Use\n\n```text\n{}\n```\n",
        use_line(path, command)
    ));
    let mut rows: BTreeMap<String, (String, String, String)> = BTreeMap::new();
    for arg in command
        .get_arguments()
        .filter(|arg| !arg.is_positional())
        .chain(globals.iter().copied())
    {
        if let Some((key, name, default, help)) = flag_row(arg) {
            rows.entry(key).or_insert((name, default, help));
        }
    }
    if !rows.is_empty() {
        out.push_str("\n## Flags\n\n| Flag | Default | Description |\n| --- | --- | --- |\n");
        for (name, default, help) in rows.values() {
            out.push_str(&format!("| `{name}` | `{default}` | {help} |\n"));
        }
    }
    let mut children: Vec<&Command> = command.get_subcommands().filter(|c| !skipped(c)).collect();
    children.sort_by_key(|child| child.get_name());
    if !children.is_empty() {
        out.push_str("\n## Subcommands\n\n");
        for child in children {
            let child_path = format!("{path} {}", child.get_name());
            let file = page_path(&child_path);
            let file = file.trim_start_matches("commands/");
            let about = child
                .get_about()
                .map(ToString::to_string)
                .unwrap_or_default();
            out.push_str(&format!("- [`{child_path}`]({file}) — {about}\n"));
        }
    }
    out
}

/// One Markdown page for each visible command, plus `commands/index.md` (Go
/// `docsite.ReferencePages`).
pub fn reference_pages(root: &Command) -> BTreeMap<String, Vec<u8>> {
    let globals: Vec<&Arg> = root
        .get_arguments()
        .filter(|arg| arg.is_global_set())
        .collect();
    let mut pages = BTreeMap::new();
    let mut index = String::from("# Command reference\n\n");
    for entry in command_tree(root) {
        let file = page_path(&entry.path);
        pages.insert(
            file.clone(),
            render_command(&entry.path, entry.command, &globals).into_bytes(),
        );
        let about = entry
            .command
            .get_about()
            .map(ToString::to_string)
            .unwrap_or_default();
        index.push_str(&format!(
            "- [`{}`]({}) — {about}\n",
            entry.path,
            file.trim_start_matches("commands/")
        ));
    }
    pages.insert("commands/index.md".into(), index.into_bytes());
    pages
}

fn render_stack(matrix: &Compatibility) -> String {
    let mut out = String::from("# Pinned stack\n\n");
    out.push_str(&format!(
        "The stack uses {} and SvelteKit {}.\n\n",
        matrix.go, matrix.sveltekit
    ));
    out.push_str("## Container images\n\n| Name | Immutable reference |\n| --- | --- |\n");
    for image in &matrix.images {
        out.push_str(&format!("| `{}` | `{}` |\n", image.name, image.reference));
    }
    out
}

/// Every generated site page: the command reference, `compat.md`, and `stack.md` (Go
/// `docsite.SitePages`). The hand-written `index.md` is not generated.
pub fn site_pages(root: &Command, matrix: &Compatibility) -> BTreeMap<String, Vec<u8>> {
    let mut pages = reference_pages(root);
    pages.insert("compat.md".into(), matrix.markdown().into_bytes());
    pages.insert("stack.md".into(), render_stack(matrix).into_bytes());
    pages
}

/// Writes `pages` below `out` through a `spinup-vfs` plan. It creates the missing
/// directories of `out`.
pub fn write_pages(out: &Path, pages: &BTreeMap<String, Vec<u8>>) -> Result<()> {
    // Apply needs an existing root. Use the nearest existing ancestor and put the missing
    // part of `out` in front of each page path.
    let mut base = out;
    while !base.as_os_str().is_empty() && !base.is_dir() {
        base = base.parent().unwrap_or(Path::new(""));
    }
    let missing = out.strip_prefix(base).unwrap_or(Path::new(""));
    let mut prefix = PathBuf::new();
    for component in missing.components() {
        match component {
            Component::Normal(part) => prefix.push(part),
            Component::CurDir => {}
            _ => anyhow::bail!(
                "invalid output directory {:?}: it has a parent reference below a missing directory",
                out
            ),
        }
    }
    let prefix = prefix.to_string_lossy().replace('\\', "/");
    let mut plan = Plan::new();
    for (path, data) in pages {
        let path = if prefix.is_empty() {
            path.clone()
        } else {
            format!("{prefix}/{path}")
        };
        plan.add(File {
            path,
            data: data.clone(),
            ..File::default()
        })?;
    }
    let root = if base.as_os_str().is_empty() {
        Path::new(".")
    } else {
        base
    };
    apply(root, &plan, ApplyOptions::default())?;
    Ok(())
}

// SAFETY: linkme places the item in its own link section. This is the registration contract of ADR 0020 §5.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static DOCS: CommandDef = CommandDef {
    name: "docs",
    group: Group::Maintain,
    build,
    run,
};

fn build() -> Command {
    Command::new("docs")
        .about("Print compatibility data and generate command references")
        .subcommand(
            Command::new("compat")
                .about("Print compatible CLI, kit, template, and stack versions")
                .arg(
                    Arg::new("markdown")
                        .long("markdown")
                        .help("write the compatibility matrix as Markdown")
                        .action(ArgAction::SetTrue),
                ),
        )
        .subcommand(
            Command::new("reference")
                .about("Write a Markdown reference page for every command")
                .arg(
                    Arg::new("out")
                        .long("out")
                        .value_name("STRING")
                        .help("directory for generated Markdown pages"),
                ),
        )
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    match matches.subcommand() {
        Some(("compat", sub)) => compat(app, sub),
        Some(("reference", sub)) => reference(app, sub),
        _ => {
            app.print_help(&["docs"]);
            Ok(())
        }
    }
}

fn compat(app: &App, matches: &ArgMatches) -> Result<()> {
    if matches.get_flag("markdown") && app.flags.json {
        return Err(UiError::new("Invalid output flags")
            .why("--json and --markdown cannot be used together")
            .try_hint("use one output flag")
            .code(exit::USAGE)
            .into());
    }
    let source = store::default(VERSION, app.flags.offline)?;
    let versions = source.versions()?;
    let matrix = Compatibility::new(VERSION, &versions, spinup_stack::load::embedded());
    if app.flags.json {
        return app.ui.json(&matrix);
    }
    app.ui.print(&matrix.markdown());
    Ok(())
}

fn reference(app: &App, matches: &ArgMatches) -> Result<()> {
    let Some(out) = matches
        .get_one::<String>("out")
        .filter(|out| !out.is_empty())
    else {
        return Err(UiError::new("Cannot write command reference")
            .why("--out is required")
            .try_hint("spinup docs reference --out <directory>")
            .code(exit::USAGE)
            .into());
    };
    let root = app.root();
    write_pages(Path::new(out), &reference_pages(&root)).map_err(|err| {
        UiError::new("Cannot write command reference")
            .why(format!("{err:#}"))
            .try_hint("check --out and its parent directory")
            .code(exit::ERROR)
    })?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::root::tests::run_args;
    use crate::root::{Defaults, build_root};

    fn repo_root() -> PathBuf {
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .ancestors()
            .nth(2)
            .expect("the workspace root")
            .to_path_buf()
    }

    /// A new empty directory below the system temporary directory.
    fn temp_dir(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "spinup-cli-{name}-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create the temporary directory");
        dir
    }

    /// Compares the pages with a site directory (Go `pagesMatch`). It fails on a generated
    /// page that has no command.
    fn pages_match(root: &Path, pages: &BTreeMap<String, Vec<u8>>) -> Result<bool, String> {
        for (path, want) in pages {
            match std::fs::read(root.join(path)) {
                Ok(got) if &got == want => {}
                Ok(_) => return Ok(false),
                Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(false),
                Err(err) => return Err(err.to_string()),
            }
        }
        for path in markdown_files(root) {
            if path != "index.md" && !pages.contains_key(&path) {
                return Err(format!("unexpected generated page {path}"));
            }
        }
        Ok(true)
    }

    fn markdown_files(root: &Path) -> Vec<String> {
        let mut out = Vec::new();
        let mut stack = vec![root.to_path_buf()];
        while let Some(dir) = stack.pop() {
            let Ok(entries) = std::fs::read_dir(&dir) else {
                continue;
            };
            for entry in entries.flatten() {
                let path = entry.path();
                if path.is_dir() {
                    stack.push(path);
                } else if path.extension().is_some_and(|ext| ext == "md") {
                    let relative = path.strip_prefix(root).expect("below the root");
                    out.push(relative.to_string_lossy().replace('\\', "/"));
                }
            }
        }
        out.sort();
        out
    }

    /// Removes generated pages that have no command. The hand-written `index.md` stays (Go
    /// `pruneStalePages`).
    fn prune_stale_pages(root: &Path, pages: &BTreeMap<String, Vec<u8>>) {
        for path in markdown_files(root) {
            if path != "index.md" && !pages.contains_key(&path) {
                std::fs::remove_file(root.join(&path)).expect("remove a stale page");
            }
        }
    }

    #[test]
    fn docs_compat_json() {
        let (code, out, err) = run_args(&["docs", "compat", "--json", "--offline"]);
        assert_eq!(code, 0, "{err}");
        assert_eq!(err, "");
        assert!(out.contains("\"postgresql_majors\""), "{out}");
        let value: serde_json::Value = serde_json::from_str(&out).expect("JSON");
        assert_eq!(
            value["postgresql_majors"],
            serde_json::json!([14, 15, 16, 17, 18])
        );
        assert!(value["images"][0]["ref"].is_string());
    }

    #[test]
    fn docs_compat_markdown() {
        let (code, out, _) = run_args(&["docs", "compat", "--markdown", "--offline"]);
        assert_eq!(code, 0);
        assert!(out.starts_with("# Compatibility\n"), "{out}");
        assert!(out.contains("| `postgres` |"), "{out}");
        let (code, _, err) = run_args(&["docs", "compat", "--markdown", "--json"]);
        assert_eq!(code, exit::USAGE);
        assert!(err.contains("\"try\":\"use one output flag\""), "{err}");
    }

    #[test]
    fn docs_reference_writes_pages() {
        let dir = temp_dir("reference");
        let out = dir.join("new/site");
        let (code, stdout, err) = run_args(&["docs", "reference", "--out", &out.to_string_lossy()]);
        assert_eq!((code, stdout.as_str(), err.as_str()), (0, "", ""));
        let page = std::fs::read_to_string(out.join("commands/docs-compat.md")).expect("page");
        assert!(page.contains("# spinup docs compat"), "{page}");
        let index = std::fs::read_to_string(out.join("commands/index.md")).expect("index");
        assert!(index.contains("[`spinup version`](version.md)"), "{index}");
        // A second run changes nothing.
        let (code, _, err) = run_args(&["docs", "reference", "--out", &out.to_string_lossy()]);
        assert_eq!(code, 0, "{err}");
        let _ = std::fs::remove_dir_all(&dir);
        let (code, _, err) = run_args(&["docs", "reference"]);
        assert_eq!(code, exit::USAGE);
        assert!(
            err.contains("Try: spinup docs reference --out <directory>"),
            "{err}"
        );
    }

    #[test]
    fn reference_page_format() {
        let root = build_root(Defaults::default());
        let pages = reference_pages(&root);
        let version = String::from_utf8(pages["commands/version.md"].clone()).expect("UTF-8");
        assert!(version.starts_with("# spinup version\n\nPrint the spinup version\n\n## Use\n\n```text\nspinup version [flags]\n```\n"), "{version}");
        assert!(version.contains("| `--check` | `false` | check for a newer stable release |\n"));
        assert!(version.contains("| `-C, --dir` | `` | run in this project directory |\n"));
        assert!(version.contains("| `--offline` | `false` | never download template versions |\n"));
        assert!(!version.contains("--help"), "{version}");
        let cache = String::from_utf8(pages["commands/cache.md"].clone()).expect("UTF-8");
        assert!(cache.contains("- [`spinup cache warm`](cache-warm.md) — Download and verify missing template versions\n"), "{cache}");
        let warm = String::from_utf8(pages["commands/cache-warm.md"].clone()).expect("UTF-8");
        assert!(warm.contains("spinup cache warm [version...]\n"), "{warm}");
        assert!(
            !pages
                .keys()
                .any(|path| path.contains("completion") || path.contains("help"))
        );
    }

    #[test]
    fn site_is_current() {
        // The --offline default reads SPINUP_OFFLINE. The site uses the plain defaults.
        let root = build_root(Defaults::default());
        let current = store::current_version(VERSION);
        let matrix = Compatibility::new(VERSION, &[current], spinup_stack::load::embedded());
        let pages = site_pages(&root, &matrix);
        let site = repo_root().join("docs/site-rs");
        if std::env::var("SPINUP_GOLDEN_UPDATE").is_ok_and(|value| value == "1") {
            prune_stale_pages(&site, &pages);
            write_pages(&site, &pages).expect("write the site");
            return;
        }
        assert_eq!(
            pages_match(&site, &pages),
            Ok(true),
            "docs/site-rs pages are stale. Try: SPINUP_GOLDEN_UPDATE=1 cargo nextest run -p spinup-cli -E 'test(site_is_current)'"
        );
    }

    #[test]
    fn prune_keeps_index() {
        let dir = temp_dir("prune");
        for name in ["index.md", "commands/old.md", "commands/kept.md"] {
            let path = dir.join(name);
            std::fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
            std::fs::write(path, "x").expect("write");
        }
        let pages = BTreeMap::from([("commands/kept.md".to_string(), b"x".to_vec())]);
        prune_stale_pages(&dir, &pages);
        assert!(!dir.join("commands/old.md").exists());
        assert_eq!(pages_match(&dir, &pages), Ok(true));
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// Every visible command appears as `spinup <path>` in a page of `docs/commands` (Go
    /// `TestEveryCommandIsDocumented`, without the doctest block check).
    #[test]
    fn every_command_is_documented() {
        let dir = repo_root().join("docs/commands");
        let mut corpus = String::new();
        for entry in std::fs::read_dir(&dir).expect("docs/commands") {
            let path = entry.expect("entry").path();
            if path.extension().is_some_and(|ext| ext == "md") {
                corpus.push_str(&std::fs::read_to_string(&path).expect("page"));
                corpus.push('\n');
            }
        }
        let root = build_root(Defaults::default());
        let mut missing = Vec::new();
        for entry in command_tree(&root) {
            if !mentions(&corpus, &entry.path, entry.command) {
                missing.push(entry.path);
            }
        }
        assert!(
            missing.is_empty(),
            "commands not documented in docs/commands: {missing:?}. Try: mention them in a page there"
        );
    }

    /// Reports whether `corpus` holds `<path>` followed by a non-word character. An alias of
    /// the last name counts too.
    fn mentions(corpus: &str, path: &str, command: &Command) -> bool {
        let (parent, _) = path.rsplit_once(' ').unwrap_or(("", path));
        let names = std::iter::once(command.get_name()).chain(command.get_all_aliases());
        names.into_iter().any(|name| {
            let needle = format!("{parent} {name}");
            corpus.match_indices(&needle).any(|(index, _)| {
                corpus[index + needle.len()..]
                    .chars()
                    .next()
                    .is_none_or(|c| !(c.is_alphanumeric() || c == '_'))
            })
        })
    }
}
