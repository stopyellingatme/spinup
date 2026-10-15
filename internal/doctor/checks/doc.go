// Package checks registers the first local doctor checks.
// Context.Deps supplies "cli_version" as a string and "runner" as an exec.Runner.
// Project checks skip when Context.Root is empty. Non-quick checks skip in quick mode.
package checks
