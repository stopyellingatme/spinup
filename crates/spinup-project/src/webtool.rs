//! Package manager and API client commands. These helpers do not execute commands.

use std::path::Path;

use spinup_exec::runner::Cmd;
use spinup_manifest::lock::Lock;

/// The package manager and API client selected for a web application.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Tool {
    bun: bool,
    heyapi: bool,
}

impl Default for Tool {
    fn default() -> Self {
        Self::new("", "")
    }
}

impl Tool {
    /// Select known tools. Missing or unknown answers use the original defaults.
    pub fn new(package_manager: &str, api_client: &str) -> Self {
        Self {
            bun: package_manager == "bun",
            heyapi: api_client == "heyapi",
        }
    }

    /// Select tools from the render answers.
    pub fn for_answers(answers: &toml::Table) -> Self {
        Self::new(
            answers
                .get("package_manager")
                .and_then(toml::Value::as_str)
                .unwrap_or(""),
            answers
                .get("api_client")
                .and_then(toml::Value::as_str)
                .unwrap_or(""),
        )
    }

    /// Select tools from a lock. A missing lock uses the original defaults.
    pub fn from_lock(lock: Option<&Lock>) -> Self {
        let Some(lock) = lock else {
            return Self::default();
        };
        // Only the ASCII names "bun" and "heyapi" select nondefault tools.
        // Every JSON spelling of these names is also a TOML basic string,
        // including Unicode escapes. Other JSON values retain the defaults.
        let answer = |key| {
            lock.answers
                .get(key)
                .and_then(|raw| {
                    toml::from_str::<toml::Table>(&format!("value = {}", raw.get())).ok()
                })
                .and_then(|mut table| table.remove("value"))
        };
        let manager = answer("package_manager");
        let client = answer("api_client");
        Self::new(
            manager.as_ref().and_then(toml::Value::as_str).unwrap_or(""),
            client.as_ref().and_then(toml::Value::as_str).unwrap_or(""),
        )
    }

    /// Return the package manager executable.
    pub fn name(self) -> &'static str {
        if self.bun { "bun" } else { "pnpm" }
    }

    /// Report whether this application uses the heyapi client.
    pub fn hey_api(self) -> bool {
        self.heyapi
    }

    /// Return the lockfile path relative to `web/`.
    pub fn lockfile(self) -> &'static str {
        if self.bun {
            "bun.lock"
        } else {
            "pnpm-lock.yaml"
        }
    }

    /// Build the install command. Bun has no offline flag.
    pub fn install(self, frozen: bool, offline: bool) -> Cmd {
        let mut cmd = Cmd::new(self.name()).args(["install"]);
        if frozen {
            cmd.args.push("--frozen-lockfile".into());
        }
        if offline && !self.bun {
            cmd.args.push("--offline".into());
        }
        cmd
    }

    /// Build the command that refreshes a lock after dependency changes.
    pub fn refresh_lock(self) -> Cmd {
        let cmd = Cmd::new(self.name()).args(["install", "--lockfile-only"]);
        if self.bun {
            cmd
        } else {
            cmd.args(["--prefer-offline", "--ignore-scripts"])
        }
    }

    /// Build a package script command with optional arguments.
    pub fn run(self, script: &str, args: &[&str]) -> Cmd {
        let cmd = Cmd::new(self.name());
        let cmd = if self.bun {
            cmd.args(["run", script])
        } else {
            cmd.args([script])
        };
        cmd.args(args.iter().copied())
    }

    /// Build a command for an installed binary without downloading packages.
    pub fn exec(self, binary: &str, args: &[&str]) -> Cmd {
        let cmd = if self.bun {
            Cmd::new("bunx").args(["--no-install", binary])
        } else {
            Cmd::new(self.name()).args(["exec", binary])
        };
        cmd.args(args.iter().copied())
    }

    /// Build a package executable command that can download missing packages.
    pub fn dlx(self, package: &str, args: &[&str]) -> Cmd {
        let cmd = if self.bun {
            Cmd::new("bunx").args([package])
        } else {
            Cmd::new(self.name()).args(["dlx", package])
        };
        cmd.args(args.iter().copied())
    }

    /// Place a command in `web/`, preserving the pnpm command spelling.
    pub fn in_web(self, root: impl AsRef<Path>, mut cmd: Cmd) -> Cmd {
        if self.bun {
            cmd.dir = Some(root.as_ref().join("web"));
        } else {
            cmd.dir = Some(root.as_ref().to_owned());
            cmd.args.splice(..0, ["--dir".into(), "web".into()]);
        }
        cmd
    }

    /// Return the generated client path relative to `web/`.
    pub fn client_output(self) -> &'static str {
        if self.heyapi {
            "src/lib/api/client"
        } else {
            "src/lib/api/schema.d.ts"
        }
    }

    /// Build the selected client generator command.
    pub fn client(self, output: &str) -> Cmd {
        if self.heyapi {
            self.exec(
                "openapi-ts",
                &[
                    "--input",
                    "../api/openapi/bundle.gen.yaml",
                    "--output",
                    output,
                ],
            )
        } else {
            self.schema(output)
        }
    }

    /// Build the shared schema generation command for resource pages.
    pub fn schema(self, output: &str) -> Cmd {
        self.exec(
            "openapi-typescript",
            &["../api/openapi/bundle.gen.yaml", "-o", output],
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_command_cases() {
        for (tool, expected) in [
            (
                Tool::default(),
                [
                    "pnpm install --frozen-lockfile --offline",
                    "pnpm check",
                    "pnpm exec vite",
                    "pnpm dlx sv@1.0.0",
                    "pnpm install --lockfile-only --prefer-offline --ignore-scripts",
                    "pnpm exec openapi-typescript ../api/openapi/bundle.gen.yaml -o output",
                ],
            ),
            (
                Tool::new("bun", "heyapi"),
                [
                    "bun install --frozen-lockfile",
                    "bun run check",
                    "bunx --no-install vite",
                    "bunx sv@1.0.0",
                    "bun install --lockfile-only",
                    "bunx --no-install openapi-ts --input ../api/openapi/bundle.gen.yaml --output output",
                ],
            ),
        ] {
            for (cmd, want) in [
                tool.install(true, true),
                tool.run("check", &[]),
                tool.exec("vite", &[]),
                tool.dlx("sv@1.0.0", &[]),
                tool.refresh_lock(),
                tool.client("output"),
            ]
            .into_iter()
            .zip(expected)
            {
                assert_eq!(cmd.to_string(), want);
            }
            let mut source = tool.run("dev", &["--host"]);
            source.redact.push("secret".into());
            let cmd = tool.in_web("/app", source);
            assert_eq!(cmd.redact, ["secret"]);
            if tool.bun {
                assert_eq!(cmd.dir, Some(Path::new("/app/web").to_owned()));
                assert_eq!(cmd.args, ["run", "dev", "--host"]);
            } else {
                assert_eq!(cmd.dir, Some(Path::new("/app").to_owned()));
                assert_eq!(cmd.args, ["--dir", "web", "dev", "--host"]);
            }
        }
    }

    #[test]
    fn independent_answers_and_defaults() {
        assert_eq!(Tool::from_lock(None), Tool::default());
        assert_eq!(Tool::for_answers(&toml::Table::new()), Tool::default());
        for (manager, client) in [
            ("bun", "heyapi"),
            ("bun", "openapi-fetch"),
            ("pnpm", "heyapi"),
            ("unknown", "unknown"),
        ] {
            let answers = toml::from_str(&format!(
                "package_manager = '{manager}'\napi_client = '{client}'"
            ))
            .unwrap();
            let tool = Tool::for_answers(&answers);
            assert_eq!(tool.name(), if manager == "bun" { "bun" } else { "pnpm" });
            assert_eq!(tool.hey_api(), client == "heyapi");
            assert_eq!(
                tool.lockfile(),
                if tool.bun {
                    "bun.lock"
                } else {
                    "pnpm-lock.yaml"
                }
            );
            assert_eq!(
                tool.client_output(),
                if tool.heyapi {
                    "src/lib/api/client"
                } else {
                    "src/lib/api/schema.d.ts"
                }
            );
            assert_eq!(tool.install(false, false).args, ["install"]);
            assert_eq!(tool.schema("out").args.last().unwrap(), "out");
            assert_eq!(
                tool.exec("vite", &["--host"]).args.last().unwrap(),
                "--host"
            );
            assert_eq!(tool.dlx("sv", &["create"]).args.last().unwrap(), "create");
        }
    }

    #[test]
    fn raw_lock_answers_and_escaped_names() {
        for (raw, expected) in [
            (
                r#"{"package_manager":"bun","api_client":"heyapi"}"#,
                Tool::new("bun", "heyapi"),
            ),
            (
                r#"{"package_manager":"b\u0075n","api_client":"hey\u0061pi"}"#,
                Tool::new("bun", "heyapi"),
            ),
            (
                r#"{"package_manager":false,"api_client":17}"#,
                Tool::default(),
            ),
            (
                r#"{"package_manager":null,"api_client":[]}"#,
                Tool::default(),
            ),
            ("{}", Tool::default()),
        ] {
            let text = format!(r#"{{"schema":3,"answers":{raw}}}"#);
            let lock = spinup_manifest::json::parse(text.as_bytes()).unwrap();
            assert_eq!(Tool::from_lock(Some(&lock)), expected);
        }
    }
}
