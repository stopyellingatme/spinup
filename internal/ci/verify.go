package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"go.yaml.in/yaml/v3"
)

// Finding describes a failed CI check at a source location.
type Finding struct {
	// Path is relative to the project root, or empty for tool output.
	Path string `json:"path"`
	// Line is the YAML source line, or zero for tool output.
	Line int `json:"line"`
	// Message describes the failed check.
	Message string `json:"message"`
}

// Report contains CI findings and notes about optional checks.
type Report struct {
	// Findings contains failed checks in source order.
	Findings []Finding `json:"findings"`
	// Notes explains omitted optional checks.
	Notes []string `json:"notes"`
}

// VerifyOptions supplies the project directory and optional tool dependencies.
type VerifyOptions struct {
	// Root is the project directory.
	Root string
	// Runner executes actionlint. A nil runner uses exec.OSRunner.
	Runner spexec.Runner
	// LookPath locates actionlint. A nil function uses exec.LookPath.
	LookPath func(string) (string, error)
}

// Verify checks workflow and composite action YAML, then runs optional actionlint.
func Verify(ctx context.Context, o VerifyOptions) (*Report, error) {
	report := &Report{Findings: []Finding{}, Notes: []string{}}
	var workflows []string
	for _, pattern := range []string{".github/workflows/*.yml", ".github/workflows/*.yaml", ".github/actions/*/action.yml", ".github/actions/*/action.yaml"} {
		files, err := filepath.Glob(filepath.Join(o.Root, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, fmt.Errorf("find CI files: %w\nTry: check the project directory", err)
		}
		for _, filename := range files {
			name, err := filepath.Rel(o.Root, filename)
			if err != nil {
				return nil, fmt.Errorf("locate CI file: %w\nTry: check the project directory", err)
			}
			name = filepath.ToSlash(name)
			data, err := os.ReadFile(filename)
			if err != nil {
				return nil, fmt.Errorf("read CI file %s: %w\nTry: check file permissions", name, err)
			}
			workflow := strings.HasPrefix(name, ".github/workflows/")
			report.Findings = append(report.Findings, verifyYAML(name, data, workflow)...)
			if workflow {
				workflows = append(workflows, name)
			}
		}
	}
	if o.LookPath == nil {
		o.LookPath = spexec.LookPath
	}
	actionlint, err := o.LookPath("actionlint")
	if err != nil {
		report.Notes = append(report.Notes, "actionlint skipped: executable is not on PATH")
	} else if len(workflows) > 0 {
		if o.Runner == nil {
			o.Runner = spexec.OSRunner{}
		}
		slices.Sort(workflows)
		result, runErr := o.Runner.Run(ctx, spexec.Cmd{Name: actionlint, Args: append([]string{"-oneline"}, workflows...), Dir: o.Root})
		output := strings.TrimSpace(string(result.Stdout) + "\n" + string(result.Stderr))
		if runErr != nil || result.ExitCode != 0 {
			if output == "" {
				output = "actionlint failed"
				if runErr != nil {
					output = runErr.Error()
				}
			}
			for _, line := range strings.Split(output, "\n") {
				if strings.TrimSpace(line) != "" {
					report.Findings = append(report.Findings, Finding{Message: line})
				}
			}
		}
	}
	slices.SortStableFunc(report.Findings, func(a, b Finding) int {
		if n := strings.Compare(a.Path, b.Path); n != 0 {
			return n
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Message, b.Message)
	})
	return report, nil
}

// Write prints a report as JSON or one line per finding, followed by notes.
func (r *Report) Write(out io.Writer, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(r)
	}
	for _, f := range r.Findings {
		message := f.Message
		if f.Path != "" {
			message = fmt.Sprintf("%s:%d: %s", f.Path, f.Line, message)
		}
		if _, err := fmt.Fprintln(out, message); err != nil {
			return err
		}
	}
	for _, note := range r.Notes {
		if _, err := fmt.Fprintln(out, "Note: "+note); err != nil {
			return err
		}
	}
	return nil
}

var actionSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func verifyYAML(name string, data []byte, workflow bool) []Finding {
	var findings []Finding
	add := func(n *yaml.Node, message string) {
		line := 1
		if n != nil && n.Line > 0 {
			line = n.Line
		}
		findings = append(findings, Finding{Path: name, Line: line, Message: strings.Join(strings.Fields(message), " ")})
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&doc); err != nil {
		add(nil, "parse YAML: "+err.Error())
		return findings
	}
	var value any
	if err := doc.Decode(&value); err != nil {
		add(nil, "parse YAML: "+err.Error())
		return findings
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		add(nil, "parse YAML: expected one document")
		return findings
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		add(nil, "parse YAML: expected a mapping")
		return findings
	}
	root := doc.Content[0]
	if workflow {
		p := yamlField(root, "permissions")
		if p == nil || p.Tag == "!!null" || (p.Kind != yaml.MappingNode && (p.Kind != yaml.ScalarNode || p.Tag != "!!str")) {
			add(root, "workflow requires top-level permissions")
		}
	}
	target := workflow && hasEvent(yamlField(root, "on"), "pull_request_target")
	visited := map[*yaml.Node]bool{}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || visited[n] {
			return
		}
		visited[n] = true
		if n.Kind == yaml.AliasNode {
			walk(n.Alias)
			return
		}
		if n.Kind == yaml.MappingNode {
			if v := yamlField(n, "uses"); v != nil {
				uses := v.Value
				_, ref, ok := strings.Cut(uses, "@")
				if !strings.HasPrefix(uses, "./") && (!ok || !actionSHA.MatchString(ref)) {
					add(v, "uses must be pinned by a 40-character SHA (local ./ actions are exempt)")
				}
				if target && strings.HasPrefix(strings.ToLower(uses), "actions/checkout@") {
					with := yamlField(n, "with")
					for _, key := range []string{"ref", "repository"} {
						if input := yamlField(with, key); input != nil && headCheckout(input.Value) {
							add(input, "pull_request_target must not use a head checkout")
							break
						}
					}
				}
			}
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(root)
	return findings
}

func yamlField(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	n = yamlResolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return yamlResolve(n.Content[i+1])
		}
	}
	// YAML merge keys can supply checkout actions and their inputs.
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Tag != "!!merge" {
			continue
		}
		merge := yamlResolve(n.Content[i+1])
		if merge.Kind == yaml.SequenceNode {
			for _, item := range merge.Content {
				if value := yamlField(item, key); value != nil {
					return value
				}
			}
		} else if value := yamlField(merge, key); value != nil {
			return value
		}
	}
	return nil
}

func yamlResolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

var bracketAccess = regexp.MustCompile(`\[['"]([a-zA-Z_][a-zA-Z_0-9]*)['"]\]`)

func headCheckout(value string) bool {
	value = strings.Join(strings.Fields(value), "")
	value = bracketAccess.ReplaceAllString(value, ".$1")
	value = strings.ToLower(value)
	// The merge commit and the PR number refs hold the untrusted PR code too.
	for _, marker := range []string{"pull_request.head", "head_ref", "refs/pull/", "merge_commit_sha", "pull_request.number", "event.number"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func hasEvent(n *yaml.Node, event string) bool {
	n = yamlResolve(n)
	if n == nil {
		return false
	}
	if n.Kind == yaml.ScalarNode {
		return n.Value == event
	}
	if n.Kind == yaml.MappingNode {
		return yamlField(n, event) != nil
	}
	for _, child := range n.Content {
		if hasEvent(child, event) {
			return true
		}
	}
	return false
}
