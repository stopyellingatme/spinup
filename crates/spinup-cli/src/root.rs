//! The root command: global flags, command groups, the usage-error mapping to exit 2, the
//! signal handler, completions, and the update notice (Go `internal/cli/root.go`).
//!
//! Seam (ADR 0020 §5): `crates/spinup/src/main.rs` calls [`execute`]. Keep its path and
//! signature.
//!
//! # Register a command
//!
//! Each file in `cmd/` adds one [`CommandDef`] to the [`COMMANDS`] slice. No unit edits a
//! central list:
//!
//! ```ignore
//! use crate::root::{App, CommandDef, Group, COMMANDS};
//!
//! // SAFETY: linkme places the item in its own link section (ADR 0020 §5).
//! #[allow(unsafe_code)]
//! #[linkme::distributed_slice(COMMANDS)]
//! static NEW: CommandDef = CommandDef { name: "new", group: Group::Develop, build, run };
//!
//! fn build() -> clap::Command {
//!     clap::Command::new("new").about("Create an application from template packs")
//! }
//!
//! fn run(app: &App, matches: &clap::ArgMatches) -> anyhow::Result<()> {
//!     app.ui.println("done");
//!     Ok(())
//! }
//! ```
//!
//! `build` returns the clap command without the global flags. The root adds them. `run` gets
//! the matches of this command. A command with subcommands dispatches them itself, and calls
//! [`App::print_help`] when no subcommand is given. A run error goes through the error text
//! mapper of `spinup-ui`. A [`spinup_ui::error::Error`] with the code
//! [`spinup_ui::exit::USAGE`] and no `Try:` hint gets `Try: <command path> --help`.

use std::ffi::OsString;
use std::io::{self, IsTerminal, Read, Write};
use std::process::ExitCode;
use std::sync::{Arc, Mutex, PoisonError};

use clap::{Arg, ArgAction, ArgMatches, Command};
use spinup_exec::os_runner::OsRunner;
use spinup_exec::runner::{Cancellation, Runner};
use spinup_ui::error::{Error as UiError, find_ui_error};
use spinup_ui::exit;
use spinup_ui::printer::{Options, Printer};
use spinup_ui::prompt::{NoPrompter, Prompter, TerminalPrompter};

/// A command group in the help output (Go `cli.Group`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Group {
    /// Create and change the app.
    Develop,
    /// Operate the database.
    Data,
    /// Build and deploy.
    Ship,
    /// Operate a deployed app.
    Operate,
    /// Maintain spinup and the app.
    Maintain,
}

impl Group {
    /// Every group, in help order.
    pub const ALL: [Group; 5] = [
        Group::Develop,
        Group::Data,
        Group::Ship,
        Group::Operate,
        Group::Maintain,
    ];

    /// The heading of the group in the help output.
    pub fn title(self) -> &'static str {
        match self {
            Group::Develop => "Develop",
            Group::Data => "Data",
            Group::Ship => "Ship",
            Group::Operate => "Operate",
            Group::Maintain => "Maintain",
        }
    }
}

/// One top-level command (Go `cli.Register`).
#[derive(Debug)]
pub struct CommandDef {
    /// The command name. It must equal the name of the command that `build` returns.
    pub name: &'static str,
    /// The help group.
    pub group: Group,
    /// Builds the clap command, without the global flags.
    pub build: fn() -> Command,
    /// Runs the command with its own matches.
    pub run: fn(&App, &ArgMatches) -> anyhow::Result<()>,
}

/// Every top-level command. Each file in `cmd/` (and `version`, `cache`, `docs`) adds one.
// SAFETY: linkme places the item in its own link section. This is the registration contract of ADR 0020 §5.
#[allow(unsafe_code)]
#[linkme::distributed_slice]
pub static COMMANDS: [CommandDef];

/// The global flags of the root command (Go `GlobalFlags`).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct GlobalFlags {
    /// `--dir`, `-C`: the project directory. Empty means the working directory.
    pub dir: String,
    /// `--env`: the target environment.
    pub env: String,
    /// `--confirm-env`: the protected environment name, typed without a terminal.
    pub confirm_env: String,
    /// `--yes`, `-y`: answer yes to prompts.
    pub yes: bool,
    /// `--non-interactive`: never prompt.
    pub non_interactive: bool,
    /// `--json`: write JSON output.
    pub json: bool,
    /// `--dry-run`: show changes without writing.
    pub dry_run: bool,
    /// `--verbose`: write extra detail.
    pub verbose: bool,
    /// `--no-color`: turn off color.
    pub no_color: bool,
    /// `--offline`: never download template versions. `SPINUP_OFFLINE=1` sets the default.
    pub offline: bool,
}

impl GlobalFlags {
    /// The printer options of these flags.
    pub fn options(&self) -> Options {
        Options {
            json: self.json,
            no_color: self.no_color,
            non_interactive: self.non_interactive,
            verbose: self.verbose,
        }
    }

    /// Reads the global flags from the matches of any command in the tree.
    pub fn from_matches(matches: &ArgMatches) -> Self {
        let text = |id: &str| {
            matches
                .try_get_one::<String>(id)
                .ok()
                .flatten()
                .cloned()
                .unwrap_or_default()
        };
        let flag = |id: &str| {
            matches
                .try_get_one::<bool>(id)
                .ok()
                .flatten()
                .copied()
                .unwrap_or(false)
        };
        Self {
            dir: text("dir"),
            env: text("env"),
            confirm_env: text("confirm-env"),
            yes: flag("yes"),
            non_interactive: flag("non-interactive"),
            json: flag("json"),
            dry_run: flag("dry-run"),
            verbose: flag("verbose"),
            no_color: flag("no-color"),
            offline: flag("offline"),
        }
    }
}

/// The settings of the root command that come from the environment.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Defaults {
    /// The default of `--offline`.
    pub offline: bool,
}

impl Defaults {
    /// Reads the defaults from the process environment (`SPINUP_OFFLINE=1`).
    pub fn from_env() -> Self {
        Self {
            offline: std::env::var("SPINUP_OFFLINE").is_ok_and(|value| value == "1"),
        }
    }
}

/// The shared state that every command gets (Go `cli.App`).
pub struct App {
    /// The console output.
    pub ui: Printer,
    /// The global flags.
    pub flags: GlobalFlags,
    /// The cancel signal. The signal handler sets it.
    pub cancel: Cancellation,
    /// The subprocess runner.
    pub runner: Arc<dyn Runner>,
    /// The environment defaults that built the command tree.
    pub defaults: Defaults,
    /// The path of the command that runs, such as `spinup cache list`.
    pub path: String,
    interactive: bool,
}

impl std::fmt::Debug for App {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("App")
            .field("flags", &self.flags)
            .field("path", &self.path)
            .finish_non_exhaustive()
    }
}

impl App {
    /// Reports whether prompts are allowed (see [`Printer::interactive`]).
    pub fn interactive(&self) -> bool {
        self.interactive
    }

    /// Returns a terminal prompter when prompts are allowed. Otherwise it returns a prompter
    /// that fails and names the missing flag.
    pub fn prompter(&self) -> Box<dyn Prompter> {
        if self.interactive {
            Box::new(TerminalPrompter)
        } else {
            Box::new(NoPrompter)
        }
    }

    /// Builds the full command tree again, for example for the docs and the completions.
    pub fn root(&self) -> Command {
        build_root(self.defaults)
    }

    /// Writes the help of the command at `path` (the names below the root) to out.
    pub fn print_help(&self, path: &[&str]) {
        let mut root = self.root();
        root.build();
        let mut command = &mut root;
        for name in path {
            if command.find_subcommand(name).is_none() {
                break;
            }
            command = command
                .find_subcommand_mut(name)
                .expect("the subcommand exists");
        }
        self.ui.print(&command.render_help().to_string());
    }
}

/// A clonable writer over a shared stream.
#[derive(Clone)]
pub struct SharedWriter(Arc<Mutex<Box<dyn Write + Send>>>);

impl SharedWriter {
    /// Wraps a writer.
    pub fn new(writer: impl Write + Send + 'static) -> Self {
        Self(Arc::new(Mutex::new(Box::new(writer))))
    }
}

impl std::fmt::Debug for SharedWriter {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("SharedWriter")
    }
}

impl Write for SharedWriter {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.0
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .write(buf)
    }

    fn flush(&mut self) -> io::Result<()> {
        self.0
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .flush()
    }
}

/// The streams, terminals, runner, and cancel signal of one CLI run. Tests build it with
/// [`Context::buffers`].
pub struct Context {
    /// The input stream.
    pub stdin: Box<dyn Read + Send>,
    /// The output stream.
    pub stdout: SharedWriter,
    /// The error stream.
    pub stderr: SharedWriter,
    /// Whether stdin, stdout, and stderr are terminals.
    pub tty: [bool; 3],
    /// The subprocess runner.
    pub runner: Arc<dyn Runner>,
    /// The cancel signal.
    pub cancel: Cancellation,
    /// The environment defaults of the command tree.
    pub defaults: Defaults,
}

impl Context {
    /// The process streams, the OS runner, and the environment defaults.
    pub fn stdio() -> Self {
        Self {
            stdin: Box::new(io::stdin()),
            stdout: SharedWriter::new(io::stdout()),
            stderr: SharedWriter::new(io::stderr()),
            tty: [
                io::stdin().is_terminal(),
                io::stdout().is_terminal(),
                io::stderr().is_terminal(),
            ],
            runner: Arc::new(OsRunner),
            cancel: Cancellation::new(),
            defaults: Defaults::from_env(),
        }
    }

    /// In-memory streams with no terminal, the OS runner, and empty defaults. It returns the
    /// buffers of stdout and stderr.
    pub fn buffers() -> (
        Self,
        spinup_ui::printer::SharedBuffer,
        spinup_ui::printer::SharedBuffer,
    ) {
        let out = spinup_ui::printer::SharedBuffer::new();
        let err = spinup_ui::printer::SharedBuffer::new();
        let context = Self {
            stdin: Box::new(io::empty()),
            stdout: SharedWriter::new(out.clone()),
            stderr: SharedWriter::new(err.clone()),
            tty: [false; 3],
            runner: Arc::new(OsRunner),
            cancel: Cancellation::new(),
            defaults: Defaults::default(),
        };
        (context, out, err)
    }
}

const GETTING_STARTED: &str = "spinup creates and operates a full-stack app.

Get started:
  spinup new <name>    Create a new app
  spinup dev           Run the app in development
  spinup --help        List all commands
";

/// The commands that the root adds itself. They have no group.
const ADDITIONAL: [(&str, &str); 2] = [
    (
        "completion",
        "Generate the autocompletion script for the specified shell",
    ),
    (
        "help",
        "Print this message or the help of the given subcommands",
    ),
];

/// Parses a boolean flag value as Go `strconv.ParseBool` does.
fn parse_bool(text: &str) -> Result<bool, String> {
    match text {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err(format!("invalid boolean value {text:?}")),
    }
}

/// A global boolean flag. As with pflag, `--flag`, `--flag=true`, and `--flag=false` work.
fn bool_flag(name: &'static str, help: &'static str, default: bool) -> Arg {
    Arg::new(name)
        .long(name)
        .help(help)
        .global(true)
        .num_args(0..=1)
        .require_equals(true)
        .default_missing_value("true")
        .default_value(if default { "true" } else { "false" })
        .value_name("BOOL")
        .value_parser(parse_bool)
        .action(ArgAction::Set)
        .hide_default_value(true)
        .hide_possible_values(true)
}

fn text_flag(name: &'static str, help: &'static str) -> Arg {
    Arg::new(name)
        .long(name)
        .help(help)
        .global(true)
        .value_name("STRING")
        .action(ArgAction::Set)
}

/// The global flags, in Go order.
fn global_flags(defaults: Defaults) -> Vec<Arg> {
    vec![
        text_flag("dir", "run in this project directory").short('C'),
        text_flag("env", "target environment"),
        bool_flag("yes", "answer yes to prompts", false).short('y'),
        text_flag(
            "confirm-env",
            "type the protected environment name without a terminal",
        ),
        bool_flag("non-interactive", "never prompt", false),
        bool_flag("json", "write JSON output", false),
        bool_flag("dry-run", "show changes without writing", false),
        bool_flag("verbose", "write extra detail", false),
        bool_flag("no-color", "turn off color", false),
        bool_flag(
            "offline",
            "never download template versions",
            defaults.offline,
        ),
    ]
}

fn completion_command() -> Command {
    Command::new("completion").about(ADDITIONAL[0].1).arg(
        Arg::new("shell")
            .required(true)
            .value_parser(clap::value_parser!(clap_complete::Shell))
            .help("the shell: bash, elvish, fish, powershell, or zsh"),
    )
}

/// The registered commands, sorted by name.
pub fn registered() -> Vec<&'static CommandDef> {
    let mut commands: Vec<&CommandDef> = COMMANDS.iter().collect();
    commands.sort_by_key(|command| command.name);
    commands
}

/// The help text of the root: the commands under their group headings. Only the groups that
/// have commands appear.
fn groups_help(commands: &[(Group, String, String)]) -> String {
    let width = commands
        .iter()
        .map(|(_, name, _)| name.len())
        .chain(ADDITIONAL.iter().map(|(name, _)| name.len()))
        .max()
        .unwrap_or(0);
    let mut text = String::new();
    for group in Group::ALL {
        let rows: Vec<_> = commands.iter().filter(|(g, _, _)| *g == group).collect();
        if rows.is_empty() {
            continue;
        }
        text.push_str(group.title());
        text.push('\n');
        for (_, name, about) in rows {
            text.push_str(&format!("  {name:<width$}  {about}\n"));
        }
        text.push('\n');
    }
    text.push_str("Additional Commands:\n");
    for (name, about) in ADDITIONAL {
        text.push_str(&format!("  {name:<width$}  {about}\n"));
    }
    text.push('\n');
    // clap reads `{` and `}` as template tags.
    text.replace(['{', '}'], "")
}

/// Builds the full command tree: the root, the global flags, every registered command, and
/// `completion` (Go `newRoot`).
pub fn build_root(defaults: Defaults) -> Command {
    let mut listed = Vec::new();
    let mut subcommands = Vec::new();
    for def in registered() {
        let command = (def.build)();
        debug_assert_eq!(command.get_name(), def.name, "CommandDef name mismatch");
        if !command.is_hide_set() {
            listed.push((
                def.group,
                command.get_name().to_string(),
                command
                    .get_about()
                    .map(ToString::to_string)
                    .unwrap_or_default(),
            ));
        }
        subcommands.push(command);
    }
    let template = format!(
        "{{about-with-newline}}\n{{usage-heading}} {{usage}}\n\n{}Options:\n{{options}}\n\nUse \"spinup [command] --help\" for more information about a command.\n",
        groups_help(&listed)
    );
    Command::new("spinup")
        .about("Create and operate a full-stack app")
        .color(clap::ColorChoice::Never)
        .help_template(template)
        .subcommand_value_name("command")
        .args(global_flags(defaults))
        .subcommands(subcommands)
        .subcommand(completion_command())
}

/// Runs the CLI with the process arguments (the program name first) and returns the exit
/// code (Go `cli.Execute`). It installs the signal handler.
pub fn execute(args: Vec<OsString>) -> ExitCode {
    let context = Context::stdio();
    install_signal_handler(&context.cancel);
    let code = run(args, context);
    ExitCode::from(u8::try_from(code).unwrap_or(1))
}

/// Installs the one signal handler of the CLI. The first SIGINT or SIGTERM cancels
/// `cancel`, so the runner stops the running subprocess. A second signal ends the process
/// with exit code 130.
#[cfg(unix)]
fn install_signal_handler(cancel: &Cancellation) {
    use signal_hook::consts::{SIGINT, SIGTERM};
    let Ok(mut signals) = signal_hook::iterator::Signals::new([SIGINT, SIGTERM]) else {
        return;
    };
    let cancel = cancel.clone();
    let _ = std::thread::Builder::new()
        .name("spinup-signals".into())
        .spawn(move || {
            for _ in signals.forever() {
                if cancel.is_cancelled() {
                    std::process::exit(130);
                }
                cancel.cancel();
            }
        });
}

#[cfg(not(unix))]
fn install_signal_handler(_cancel: &Cancellation) {}

/// Reads the output flags from raw arguments, for errors before the parse (Go `Execute`).
fn raw_options(args: &[OsString]) -> Options {
    let mut options = Options::default();
    for arg in args.iter().skip(1) {
        match arg.to_str() {
            Some("--") => break,
            Some("--json" | "--json=true") => options.json = true,
            Some("--no-color" | "--no-color=true") => options.no_color = true,
            _ => {}
        }
    }
    options
}

/// Returns true when the argument takes its value from the next argument.
fn takes_next(arg: &Arg) -> bool {
    arg.get_action().takes_values() && !arg.is_require_equals_set()
}

/// Finds the command path that the raw arguments name, such as `spinup cache warm`. It stops
/// at the first word that is not a subcommand.
pub fn usage_path(root: &Command, args: &[OsString]) -> String {
    let mut path = vec![root.get_name().to_string()];
    let globals: Vec<&Arg> = root.get_arguments().filter(|a| a.is_global_set()).collect();
    let mut command = root;
    let mut rest = args.iter().skip(1);
    while let Some(arg) = rest.next() {
        let text = arg.to_string_lossy();
        let known = |find: &dyn Fn(&Arg) -> bool| {
            command
                .get_arguments()
                .chain(globals.iter().copied())
                .find(|a| find(a))
                .is_some_and(takes_next)
        };
        if text == "--" {
            break;
        }
        if let Some(long) = text.strip_prefix("--") {
            if !long.contains('=') && known(&|a: &Arg| a.get_long() == Some(long)) {
                rest.next();
            }
            continue;
        }
        if let Some(shorts) = text.strip_prefix('-')
            && !shorts.is_empty()
        {
            let chars: Vec<char> = shorts.chars().collect();
            for (index, c) in chars.iter().enumerate() {
                if known(&|a: &Arg| a.get_short() == Some(*c)) {
                    if index + 1 == chars.len() {
                        rest.next();
                    }
                    break;
                }
            }
            continue;
        }
        match command.find_subcommand(&*text) {
            Some(sub) => {
                path.push(sub.get_name().to_string());
                command = sub;
            }
            None => break,
        }
    }
    path.join(" ")
}

/// Maps a clap parse error to a usage error with exit code 2.
fn usage_error(err: &clap::Error, path: &str) -> anyhow::Error {
    let text = err.to_string();
    let why = text
        .lines()
        .next()
        .unwrap_or_default()
        .trim_start_matches("error: ")
        .trim()
        .to_string();
    let flag = matches!(
        err.get(clap::error::ContextKind::InvalidArg),
        Some(clap::error::ContextValue::String(arg)) if arg.starts_with('-')
    );
    UiError::new(if flag {
        "Invalid flag"
    } else {
        "Invalid usage"
    })
    .why(why)
    .try_hint(format!("{path} --help"))
    .code(exit::USAGE)
    .into()
}

/// Adds `Try: <path> --help` to a usage error without a hint (Go `Execute`).
fn with_usage_hint(err: anyhow::Error, path: &str) -> anyhow::Error {
    match find_ui_error(&err) {
        Some(ue) if ue.code == exit::USAGE && ue.try_hint.is_empty() => {
            UiError::new(ue.what.clone())
                .why(ue.why.clone())
                .try_hint(format!("{path} --help"))
                .code(exit::USAGE)
                .into()
        }
        _ => err,
    }
}

/// The release notice source. It returns the notice line when a newer release exists.
///
/// R1 has no release check, so it returns `None`. R4 binds `spinup-selfupdate` here (the
/// daily check with a two-second deadline, Go `selfupdate.Notice`).
fn release_notice(_current: &str) -> Option<String> {
    None
}

/// Reports whether the silent update notice may run (Go `printUpdateNotice` and
/// `selfupdate.Notice`): a terminal, no `--json`, no `--offline`, no `CI`, no
/// `SPINUP_NO_UPDATE_CHECK=1`, and not an explicit release check.
pub fn notice_allowed(app: &App, matches: &ArgMatches) -> bool {
    let explicit = match matches.subcommand() {
        Some(("self-update", _)) => true,
        Some(("version", sub)) => sub.try_get_one::<bool>("check").ok().flatten() == Some(&true),
        _ => false,
    };
    let ci = std::env::var_os("CI").is_some();
    let disabled = std::env::var("SPINUP_NO_UPDATE_CHECK").is_ok_and(|v| v == "1");
    !explicit && app.interactive() && !app.flags.json && !app.flags.offline && !ci && !disabled
}

/// Runs the CLI with explicit streams and returns the exit code. [`execute`] calls it with
/// the process streams. Tests call it with [`Context::buffers`].
pub fn run(args: Vec<OsString>, context: Context) -> i32 {
    let Context {
        stdin,
        stdout,
        stderr,
        tty,
        runner,
        cancel,
        defaults,
    } = context;
    let mut root = build_root(defaults);
    let matches = match root.try_get_matches_from_mut(args.iter().cloned()) {
        Ok(matches) => matches,
        Err(err) => {
            use clap::error::ErrorKind;
            if matches!(
                err.kind(),
                ErrorKind::DisplayHelp
                    | ErrorKind::DisplayVersion
                    | ErrorKind::DisplayHelpOnMissingArgumentOrSubcommand
            ) {
                let mut out = stdout;
                let _ = write!(out, "{err}");
                return exit::OK;
            }
            let path = usage_path(&root, &args);
            let error = usage_error(&err, &path);
            Printer::new(io::empty(), stdout, stderr, raw_options(&args))
                .with_tty(tty[0], tty[1], tty[2])
                .render_error(&error);
            return exit::USAGE;
        }
    };

    // The deepest matches hold every global flag.
    let mut path = vec!["spinup".to_string()];
    let mut leaf = &matches;
    while let Some((name, sub)) = leaf.subcommand() {
        path.push(name.to_string());
        leaf = sub;
    }
    let flags = GlobalFlags::from_matches(leaf);
    let ui = Printer::new(stdin, stdout, stderr, flags.options()).with_tty(tty[0], tty[1], tty[2]);
    let interactive = ui.interactive();
    let app = App {
        ui,
        flags,
        cancel,
        runner,
        defaults,
        path: path.join(" "),
        interactive,
    };
    if notice_allowed(&app, &matches)
        && let Some(notice) = release_notice(crate::version::VERSION)
    {
        app.ui.warn(&notice);
    }

    let result = match matches.subcommand() {
        None => {
            app.ui.print(GETTING_STARTED);
            Ok(())
        }
        Some(("completion", sub)) => run_completion(&app, sub),
        Some((name, sub)) => match COMMANDS.iter().find(|def| def.name == name) {
            Some(def) => (def.run)(&app, sub),
            None => Err(anyhow::anyhow!(
                "command {name:?} has no runner\nTry: report this bug"
            )),
        },
    };
    match result {
        Ok(()) => exit::OK,
        Err(err) => {
            let err = with_usage_hint(err, &app.path);
            app.ui.render_error(&err);
            exit::exit_code(&err)
        }
    }
}

fn run_completion(app: &App, matches: &ArgMatches) -> anyhow::Result<()> {
    let shell = *matches
        .get_one::<clap_complete::Shell>("shell")
        .ok_or_else(|| {
            UiError::new("Missing shell")
                .why("completion needs a shell name")
                .code(exit::USAGE)
        })?;
    let mut root = app.root();
    let mut script = Vec::new();
    clap_complete::generate(shell, &mut root, "spinup", &mut script);
    app.ui.print(&String::from_utf8_lossy(&script));
    Ok(())
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    /// Runs the CLI with in-memory streams. It returns the exit code, stdout, and stderr.
    pub(crate) fn run_args(args: &[&str]) -> (i32, String, String) {
        let (context, out, err) = Context::buffers();
        let mut argv = vec![OsString::from("spinup")];
        argv.extend(args.iter().map(OsString::from));
        let code = run(argv, context);
        (code, out.contents(), err.contents())
    }

    #[test]
    fn no_args_card() {
        let (code, out, _) = run_args(&[]);
        assert_eq!(code, exit::OK);
        for text in ["spinup new <name>", "spinup dev", "spinup --help"] {
            assert!(out.contains(text), "card misses {text:?}: {out}");
        }
        assert!(out.starts_with("spinup creates and operates a full-stack app."));
    }

    #[test]
    fn usage_errors() {
        for (args, hint) in [
            (vec!["nope"], "Try: spinup --help"),
            (vec!["--nope"], "Try: spinup --help"),
            (vec!["version", "--nope"], "Try: spinup version --help"),
            (vec!["version", "extra"], "Try: spinup version --help"),
            (vec!["cache", "nope"], "Try: spinup cache --help"),
            (
                vec!["-C", "x", "version", "--json=maybe"],
                "Try: spinup version --help",
            ),
        ] {
            let (code, out, err) = run_args(&args);
            assert_eq!(code, exit::USAGE, "{args:?}: {err}");
            assert_eq!(out, "", "{args:?}");
            assert!(err.starts_with("Error: "), "{args:?}: {err}");
            assert!(err.contains(hint), "{args:?}: {err}");
        }
        let (_, _, err) = run_args(&["--nope"]);
        assert!(err.starts_with("Error: Invalid flag\n"), "{err}");
        let (_, _, err) = run_args(&["nope"]);
        assert!(err.starts_with("Error: Invalid usage\n"), "{err}");
        assert!(err.contains("nope"), "{err}");
    }

    #[test]
    fn usage_error_json() {
        let (code, _, err) = run_args(&["--json", "nope"]);
        assert_eq!(code, exit::USAGE);
        let value: serde_json::Value = serde_json::from_str(&err).expect("JSON error");
        assert_eq!(value["error"]["code"], exit::USAGE);
        assert_eq!(value["error"]["try"], "spinup --help");
        assert_eq!(value["error"]["what"], "Invalid usage");
    }

    #[test]
    fn help_shows_only_used_groups() {
        let (code, out, _) = run_args(&["--help"]);
        assert_eq!(code, exit::OK);
        let used: Vec<Group> = registered().iter().map(|def| def.group).collect();
        for group in Group::ALL {
            let heading = format!("\n{}\n", group.title());
            assert_eq!(
                out.contains(&heading),
                used.contains(&group),
                "group {group:?}:\n{out}"
            );
        }
        assert!(out.contains("\n  version "), "{out}");
        assert!(out.contains("Additional Commands:\n  completion"), "{out}");
        assert!(out.contains("--offline"), "{out}");
        let (code, out, _) = run_args(&["help", "version"]);
        assert_eq!(code, exit::OK);
        assert!(out.contains("Print the spinup version"), "{out}");
    }

    #[test]
    fn commands_are_sorted_and_unique() {
        let names: Vec<&str> = registered().iter().map(|def| def.name).collect();
        let mut sorted = names.clone();
        sorted.sort_unstable();
        sorted.dedup();
        assert_eq!(names, sorted);
        for def in registered() {
            assert_eq!((def.build)().get_name(), def.name);
        }
        let root = build_root(Defaults::default());
        root.clone().debug_assert();
        let order: Vec<&str> = root.get_subcommands().map(Command::get_name).collect();
        assert_eq!(
            order,
            build_root(Defaults::default())
                .get_subcommands()
                .map(Command::get_name)
                .collect::<Vec<_>>()
        );
    }

    #[test]
    fn version_is_in_maintain() {
        let def = registered()
            .into_iter()
            .find(|def| def.name == "version")
            .expect("version is registered");
        assert_eq!(def.group, Group::Maintain);
    }

    #[test]
    fn global_flags_parse_like_pflag() {
        let root = build_root(Defaults::default());
        let matches = root
            .try_get_matches_from([
                "spinup",
                "version",
                "--offline",
                "--json=false",
                "-C",
                "dir",
                "--env=prod",
                "-y",
                "--dry-run=1",
            ])
            .expect("parse");
        let (_, sub) = matches.subcommand().expect("subcommand");
        let flags = GlobalFlags::from_matches(sub);
        assert_eq!(
            flags,
            GlobalFlags {
                dir: "dir".into(),
                env: "prod".into(),
                yes: true,
                dry_run: true,
                offline: true,
                ..GlobalFlags::default()
            }
        );
        let root = build_root(Defaults { offline: true });
        let matches = root
            .clone()
            .try_get_matches_from(["spinup", "version"])
            .expect("parse");
        assert!(GlobalFlags::from_matches(matches.subcommand().expect("sub").1).offline);
        let matches = root
            .try_get_matches_from(["spinup", "version", "--offline=false"])
            .expect("parse");
        assert!(!GlobalFlags::from_matches(matches.subcommand().expect("sub").1).offline);
    }

    #[test]
    fn usage_path_skips_flag_values() {
        let root = build_root(Defaults::default());
        let path = |args: &[&str]| {
            let mut argv = vec![OsString::from("spinup")];
            argv.extend(args.iter().map(OsString::from));
            usage_path(&root, &argv)
        };
        assert_eq!(path(&["--dir", "cache", "version"]), "spinup version");
        assert_eq!(path(&["-C", "cache", "version"]), "spinup version");
        assert_eq!(path(&["--json", "cache", "list"]), "spinup cache list");
        assert_eq!(path(&["--dir=x", "nope", "version"]), "spinup");
        assert_eq!(path(&["--", "version"]), "spinup");
    }

    #[test]
    fn run_errors_get_usage_hint() {
        let plain: anyhow::Error = UiError::new("Bad").code(exit::USAGE).into();
        let err = with_usage_hint(plain, "spinup docs");
        assert_eq!(
            find_ui_error(&err).expect("ui").try_hint,
            "spinup docs --help"
        );
        let own: anyhow::Error = UiError::new("Bad").try_hint("x").code(exit::USAGE).into();
        assert_eq!(
            find_ui_error(&with_usage_hint(own, "p"))
                .expect("ui")
                .try_hint,
            "x"
        );
        let other: anyhow::Error = UiError::new("Bad").into();
        assert_eq!(
            find_ui_error(&with_usage_hint(other, "p"))
                .expect("ui")
                .try_hint,
            ""
        );
    }

    #[test]
    fn completion_writes_a_script() {
        let (code, out, err) = run_args(&["completion", "bash"]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(out.contains("spinup"), "{out}");
        let (code, _, err) = run_args(&["completion", "nope"]);
        assert_eq!(code, exit::USAGE, "{err}");
        assert!(err.contains("Try: spinup completion --help"), "{err}");
    }

    #[test]
    fn notice_rules() {
        let (context, _, _) = Context::buffers();
        let root = build_root(Defaults::default());
        let matches = root
            .try_get_matches_from(["spinup", "version"])
            .expect("parse");
        let app = App {
            ui: Printer::new(
                io::empty(),
                context.stdout,
                context.stderr,
                Options::default(),
            ),
            flags: GlobalFlags::default(),
            cancel: context.cancel,
            runner: context.runner,
            defaults: Defaults::default(),
            path: "spinup version".into(),
            interactive: false,
        };
        // No terminal: no notice.
        assert!(!notice_allowed(&app, &matches));
        let app = App {
            interactive: true,
            ..app
        };
        let ci = std::env::var_os("CI").is_some()
            || std::env::var("SPINUP_NO_UPDATE_CHECK").is_ok_and(|v| v == "1");
        assert_eq!(notice_allowed(&app, &matches), !ci);
        let check = build_root(Defaults::default())
            .try_get_matches_from(["spinup", "version", "--check"])
            .expect("parse");
        assert!(!notice_allowed(&app, &check));
        let app = App {
            flags: GlobalFlags {
                json: true,
                ..GlobalFlags::default()
            },
            ..app
        };
        assert!(!notice_allowed(&app, &matches));
        assert_eq!(release_notice("dev"), None);
    }

    #[test]
    fn parse_bool_matches_go() {
        for text in ["1", "t", "T", "TRUE", "true", "True"] {
            assert_eq!(parse_bool(text), Ok(true));
        }
        for text in ["0", "f", "F", "FALSE", "false", "False"] {
            assert_eq!(parse_bool(text), Ok(false));
        }
        assert!(parse_bool("yes").is_err());
    }
}
