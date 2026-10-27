//! Commands for the pinned shadcn-svelte generator.
use anyhow::{Result, bail};
use spinup_gen::contract::{Command, Generator, Input, Output};
use spinup_project::webtool::Tool;

/// Default shadcn registry version for older locks.
pub const DEFAULT_REGISTRY: &str = "1.7.0";

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static COMPONENT: Generator = Generator {
    name: "component",
    aliases: &[],
    short: "Add shadcn-svelte components to the web app (needs the network)",
    usage: "<name>...",
    args: |cmd| cmd,
    run: plan,
};

/// Returns the pnpm command that adds the requested components in `web/`.
pub fn command(registry_version: &str, names: &[String]) -> Command {
    component_command(Tool::default(), registry_version, names)
}
fn component_command(tool: Tool, version: &str, names: &[String]) -> Command {
    let mut args = vec!["add"];
    args.extend(names.iter().map(String::as_str));
    args.extend(["--yes", "--no-deps-install"]);
    let cmd = tool.dlx(&format!("shadcn-svelte@{version}"), &args);
    Command {
        dir: "web".into(),
        argv: std::iter::once(cmd.program).chain(cmd.args).collect(),
    }
}
fn valid_name(name: &str) -> bool {
    name.starts_with(|c: char| c.is_ascii_lowercase())
        && name.split('-').all(|p| {
            !p.is_empty()
                && p.bytes()
                    .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit())
        })
}
fn plan(input: &Input<'_>) -> Result<Output> {
    if input.args.is_empty() {
        bail!(
            "expected at least one component name\nTry: spinup g component dialog (the names are at https://www.shadcn-svelte.com/docs/components)"
        );
    }
    if !input.lock.packs.iter().any(|p| p == "web") {
        bail!(
            "the app has no web pack, so it has no web/src/lib/components\nTry: add components in an app made without --api-only"
        );
    }
    for name in input.args {
        if !valid_name(name) {
            bail!(
                "component name {name:?} is invalid: use lower-case letters, digits, and hyphens\nTry: spinup g component dialog dropdown-menu"
            );
        }
    }
    let mut registry = DEFAULT_REGISTRY.to_string();
    if let Some(raw) = input.lock.answers.get("shadcn_registry") {
        registry = crate::pages::answer(input.lock, "shadcn_registry").unwrap_or_default();
        let segments: Vec<_> = registry.split('.').collect();
        if segments.len() != 3
            || !segments
                .iter()
                .all(|s| !s.is_empty() && s.bytes().all(|c| c.is_ascii_digit()))
        {
            bail!(
                "the lock answer shadcn_registry is {}, not a version such as {DEFAULT_REGISTRY}\nTry: correct shadcn_registry in spinup.lock",
                raw.get()
            );
        }
    }
    let tool = Tool::from_lock(Some(input.lock));
    Ok(Output {
        commands: vec![component_command(tool, &registry, input.args)],
        next: vec![
            format!("cd web && {}", tool.install(false, false)),
            format!("cd web && {}", tool.run("check", &[])),
        ],
        ..Output::default()
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn component_commands_and_paths() {
        let names = ["dialog".into(), "dropdown-menu".into()];
        assert_eq!(
            command("1.7.0", &names).argv,
            [
                "pnpm",
                "dlx",
                "shadcn-svelte@1.7.0",
                "add",
                "dialog",
                "dropdown-menu",
                "--yes",
                "--no-deps-install"
            ]
        );
        let bun = component_command(Tool::new("bun", ""), "2.0.0", &names);
        assert_eq!(bun.dir, "web");
        assert_eq!(&bun.argv[..2], ["bunx", "shadcn-svelte@2.0.0"]);
        for name in ["button", "dropdown-menu", "x2"] {
            assert!(valid_name(name));
        }
        for name in ["", "../x", "Button", "-a", "a--b", "2x", "a_b"] {
            assert!(!valid_name(name));
        }
    }
}
