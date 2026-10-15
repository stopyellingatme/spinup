// Package docsite generates the docs site pages and the compatibility matrix (ADR 0014 §8).
package docsite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

// BehaviorVersions lists the behavior versions supported by this CLI.
var BehaviorVersions = []int{1}

// Compatibility reports versions that work together.
type Compatibility struct {
	CLIVersion       string   `json:"cli_version"`
	KitVersionRule   string   `json:"kit_version_rule"`
	TemplateVersions []string `json:"template_versions"`
	BehaviorVersions []int    `json:"behavior_versions"`
	PostgreSQLMajors []int    `json:"postgresql_majors"`
	Go               string   `json:"go"`
	Node             string   `json:"node"`
	SvelteKit        string   `json:"sveltekit"`
	Images           []Image  `json:"images"`
}

// Image is one immutable container image pin.
type Image struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// NewCompatibility builds the compatibility matrix from supplied template versions and pinned stack data.
func NewCompatibility(cliVersion string, templateVersions []string, stack *stackversions.Stack) Compatibility {
	images := make([]Image, 0, len(stack.Images))
	for name, image := range stack.Images {
		images = append(images, Image{Name: name, Ref: image.Ref()})
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	return Compatibility{
		CLIVersion:       cliVersion,
		KitVersionRule:   "CLI and kit major and minor versions must match.",
		TemplateVersions: compat.SupportedTemplateVersions(cliVersion, templateVersions),
		BehaviorVersions: append([]int(nil), BehaviorVersions...),
		PostgreSQLMajors: postgreSQLMajors(),
		Go:               stack.Go.Toolchain + " (generated apps require Go " + stack.Go.Min + ")",
		Node:             strings.TrimPrefix(stack.Images["node"].Tag, "v"),
		SvelteKit:        stack.NPM["@sveltejs/kit"],
		Images:           images,
	}
}

func postgreSQLMajors() []int {
	majors := []int{}
	for major := 1; major <= 99; major++ {
		check, err := ops.CheckServerVersion(fmt.Sprintf("%d0000", major))
		if err == nil && check.Fail == "" {
			majors = append(majors, major)
		}
	}
	return majors
}

// JSON returns the compatibility matrix as indented JSON.
func (c Compatibility) JSON() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// Markdown returns the compatibility matrix as Markdown.
func (c Compatibility) Markdown() string {
	var out strings.Builder
	out.WriteString("# Compatibility\n\n")
	fmt.Fprintf(&out, "- CLI version: `%s`\n", c.CLIVersion)
	fmt.Fprintf(&out, "- Kit rule: %s\n", c.KitVersionRule)
	fmt.Fprintf(&out, "- Template versions: %s\n", markdownList(c.TemplateVersions))
	fmt.Fprintf(&out, "- Behavior versions: %s\n", intList(c.BehaviorVersions))
	fmt.Fprintf(&out, "- PostgreSQL majors: %s\n", intList(c.PostgreSQLMajors))
	fmt.Fprintf(&out, "- Go: %s\n", c.Go)
	fmt.Fprintf(&out, "- Node: %s\n", c.Node)
	fmt.Fprintf(&out, "- SvelteKit: %s\n\n", c.SvelteKit)
	out.WriteString("## Pinned images\n\n")
	out.WriteString("| Name | Image |\n| --- | --- |\n")
	for _, image := range c.Images {
		fmt.Fprintf(&out, "| `%s` | `%s` |\n", image.Name, image.Ref)
	}
	return out.String()
}

// ReferencePages returns deterministic Markdown pages for every visible Cobra command.
func ReferencePages(root *cobra.Command) map[string][]byte {
	pages := map[string][]byte{}
	commands := commandTree(root)
	index := &strings.Builder{}
	index.WriteString("# Command reference\n\n")
	for _, command := range commands {
		path := commandPath(command)
		pages[path] = []byte(renderCommand(command))
		fmt.Fprintf(index, "- [`%s`](%s) — %s\n", command.CommandPath(), strings.TrimPrefix(path, "commands/"), command.Short)
	}
	pages["commands/index.md"] = []byte(index.String())
	return pages
}

// SitePages returns every generated site page.
func SitePages(root *cobra.Command, matrix Compatibility) map[string][]byte {
	pages := ReferencePages(root)
	pages["compat.md"] = []byte(matrix.Markdown())
	pages["stack.md"] = []byte(renderStack(matrix))
	return pages
}

func commandTree(root *cobra.Command) []*cobra.Command {
	var commands []*cobra.Command
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		if command != root && !command.Hidden {
			commands = append(commands, command)
		}
		children := command.Commands()
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			visit(child)
		}
	}
	visit(root)
	sort.Slice(commands, func(i, j int) bool { return commands[i].CommandPath() < commands[j].CommandPath() })
	return commands
}

func commandPath(command *cobra.Command) string {
	parts := strings.Fields(command.CommandPath())
	if len(parts) > 0 && parts[0] == "spinup" {
		parts = parts[1:]
	}
	return filepath.ToSlash(filepath.Join("commands", strings.Join(parts, "-"))) + ".md"
}

func renderCommand(command *cobra.Command) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", command.CommandPath())
	fmt.Fprintf(&out, "%s\n\n", command.Short)
	if command.Long != "" && command.Long != command.Short {
		fmt.Fprintf(&out, "%s\n\n", command.Long)
	}
	out.WriteString("## Use\n\n```text\n")
	out.WriteString(command.UseLine())
	out.WriteString("\n```\n")
	flags := commandFlags(command)
	if len(flags) > 0 {
		out.WriteString("\n## Flags\n\n| Flag | Default | Description |\n| --- | --- | --- |\n")
		for _, flag := range flags {
			name := "--" + flag.Name
			if flag.Shorthand != "" {
				name = "-" + flag.Shorthand + ", " + name
			}
			fmt.Fprintf(&out, "| `%s` | `%s` | %s |\n", name, flag.DefValue, strings.ReplaceAll(flag.Usage, "|", "\\|"))
		}
	}
	children := command.Commands()
	visible := make([]*cobra.Command, 0, len(children))
	for _, child := range children {
		if !child.Hidden {
			visible = append(visible, child)
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].Name() < visible[j].Name() })
	if len(visible) > 0 {
		out.WriteString("\n## Subcommands\n\n")
		for _, child := range visible {
			fmt.Fprintf(&out, "- [`%s`](%s) — %s\n", child.CommandPath(), filepath.Base(commandPath(child)), child.Short)
		}
	}
	if command.Example != "" {
		out.WriteString("\n## Examples\n\n```text\n")
		out.WriteString(strings.TrimSpace(command.Example))
		out.WriteString("\n```\n")
	}
	return out.String()
}

func commandFlags(command *cobra.Command) []*pflag.Flag {
	flags := []*pflag.Flag{}
	command.NonInheritedFlags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag) })
	command.InheritedFlags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag) })
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	return flags
}

func renderStack(matrix Compatibility) string {
	var out strings.Builder
	out.WriteString("# Pinned stack\n\n")
	fmt.Fprintf(&out, "The stack uses %s and SvelteKit %s.\n\n", matrix.Go, matrix.SvelteKit)
	out.WriteString("## Container images\n\n| Name | Immutable reference |\n| --- | --- |\n")
	for _, image := range matrix.Images {
		fmt.Fprintf(&out, "| `%s` | `%s` |\n", image.Name, image.Ref)
	}
	return out.String()
}

func markdownList(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = "`" + value + "`"
	}
	return strings.Join(quoted, ", ")
}

func intList(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = fmt.Sprint(value)
	}
	return strings.Join(parts, ", ")
}
