package ci_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ci"
	spexec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestVerify(t *testing.T) {
	t.Parallel()
	const pinned = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
	for _, tc := range []struct{ name, source, want string }{
		{"clean", "on: push\npermissions: {contents: read}\njobs:\n  test:\n    steps:\n      - uses: " + pinned + "\n      - uses: ./.github/actions/setup-spinup\n", ""},
		{"unpinned", "on: push\npermissions: {}\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n", "40-character SHA"},
		{"permissions", "on: push\njobs: {}\n", "top-level permissions"},
		{"head", "on: [pull_request_target]\npermissions: {}\njobs:\n  test:\n    steps:\n      - uses: " + pinned + "\n        with:\n          ref: ${{ github.event.pull_request.head.sha }}\n", "head checkout"},
		{"head repository", "on: {pull_request_target: {}}\npermissions: {}\njobs:\n  test:\n    steps:\n      - uses: " + pinned + "\n        with:\n          repository: ${{ github.event.pull_request.head.repo.full_name }}\n", "head checkout"},
		{"base checkout", "on: pull_request_target\npermissions: {}\njobs:\n  test:\n    steps:\n      - uses: " + pinned + "\n        with:\n          ref: ${{ github.event.pull_request.base.sha }}\n", ""},
		{"aliased head", "on: &event pull_request_target\npermissions: {}\nenv:\n  HEAD: &head ${{ github.event.pull_request.head.sha }}\njobs:\n  test:\n    steps:\n      - uses: " + pinned + "\n        with:\n          ref: *head\n", "head checkout"},
		{"bracket head", "on: pull_request_target\npermissions: {}\njobs:\n  test:\n    steps:\n      - uses: " + pinned + "\n        with:\n          ref: ${{ github['event']['pull_request']['head']['sha'] }}\n", "head checkout"},
		{"aliased event", "name: &event pull_request_target\non: *event\npermissions: {}\njobs:\n  test:\n    steps:\n      - uses: " + pinned + "\n        with:\n          ref: ${{ github.head_ref }}\n", "head checkout"},
		{"merged head", "on: pull_request_target\npermissions: {}\nenv: &checkout\n  uses: " + pinned + "\njobs:\n  test:\n    steps:\n      - <<: *checkout\n        with:\n          ref: ${{ github.head_ref }}\n", "head checkout"},
		{"composite", "runs:\n  using: composite\n  steps:\n    - uses: actions/checkout@v4\n", "40-character SHA"},
		{"malformed", "jobs: [", "parse YAML"},
		{"duplicate", "permissions: {}\npermissions: {}\n", "parse YAML"},
		{"null permissions", "permissions:\njobs: {}\n", "top-level permissions"},
		{"script text", "on: push\npermissions: {}\njobs:\n  test:\n    steps:\n      - run: 'echo uses: actions/checkout@v4'\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			name := ".github/workflows/test.yml"
			if tc.name == "composite" {
				name = ".github/actions/test/action.yml"
			}
			file := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(tc.source), 0644); err != nil {
				t.Fatal(err)
			}
			report, err := ci.Verify(context.Background(), ci.VerifyOptions{Root: root, LookPath: func(string) (string, error) { return "", &spexec.NotFoundError{Name: "actionlint"} }})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && len(report.Findings) != 0 {
				t.Fatalf("findings: %v", report.Findings)
			}
			if tc.want != "" && (len(report.Findings) == 0 || !strings.Contains(report.Findings[0].Message, tc.want)) {
				t.Fatalf("findings %v, want %q", report.Findings, tc.want)
			}
			if len(report.Notes) != 1 {
				t.Fatalf("notes: %v", report.Notes)
			}
		})
	}
}

func TestVerifyActionlint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stdout, stderr string
		code                 int
	}{
		{name: "clean"}, {name: "findings", stdout: "test.yml:1:1: invalid workflow\n", code: 1}, {name: "failure", stderr: "could not read config\n", code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeCIFile(t, filepath.Join(root, ".github/workflows/test.yml"), "on: push\npermissions: {}\njobs: {}\n", 0644)
			runner := new(spexec.Fake)
			runner.Expect("actionlint", "-oneline", ".github/workflows/test.yml").Return(tc.stdout, tc.stderr, tc.code)
			report, err := ci.Verify(context.Background(), ci.VerifyOptions{Root: root, Runner: runner, LookPath: func(string) (string, error) { return "actionlint", nil }})
			if err != nil {
				t.Fatal(err)
			}
			if (len(report.Findings) > 0) != (tc.code != 0) {
				t.Fatalf("findings: %v", report.Findings)
			}
			runner.AssertDone(t)
		})
	}
}
