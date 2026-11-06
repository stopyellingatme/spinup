package migrate_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/migrate"
	"github.com/stopyellingatme/spinup/internal/migrate/migratetest"
)

const oldKitTest = `package handlers

import (
 "testing"
 "example.com/demo/api/db/migrations"
 "github.com/stopyellingatme/spinup/kit/testkit"
)

// TestExample keeps this comment.
func TestExample(t *testing.T) {
 // Keep the setup comment.
 pool := testkit.DBWithMigrations(t, migrations.FS)
 _ = pool
 _ = testkit.DBWithMigrations(t, migrations.FS)
 // This string is not a call.
 _ = "testkit.DBWithMigrations(t, migrations.FS)"
}
`

func TestKitTestkitMigration(t *testing.T) {
	t.Parallel()
	m := kitMigration(t, "0004_kit_testkit")
	if m.Stage != migrate.After || m.Since != "0.1.0-alpha.420" {
		t.Fatalf("migration = %+v", m)
	}
	root := writeTree(t, map[string]string{"api/db/kit/embed.go": "package kit\n", "api/internal/handlers/example_test.go": oldKitTest})
	path := filepath.Join(root, "api/internal/handlers/example_test.go")
	c := &migrate.Ctx{Root: root, Runner: execx.OSRunner{}}
	changes, err := m.Plan(c)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %+v, error = %v", changes, err)
	}
	before, _ := os.ReadFile(path)
	if string(before) != oldKitTest {
		t.Fatal("Plan changed the file")
	}
	if err := m.Apply(c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`kitmigrations "example.com/demo/api/db/kit"`, "testkit.DBWithMigrations(t, migrations.FS, testkit.WithKitMigrations(kitmigrations.FS))", "// TestExample keeps this comment.", "// Keep the setup comment.", `"testkit.DBWithMigrations(t, migrations.FS)"`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
	formatted, err := (execx.OSRunner{}).Run(t.Context(), execx.Cmd{Name: "gofmt", Stdin: bytes.NewReader(data)})
	if err != nil || !bytes.Equal(data, formatted.Stdout) {
		t.Fatal("not formatted", err)
	}
	changes, err = m.Plan(c)
	if err != nil || len(changes) != 0 {
		t.Fatalf("second plan = %+v, error = %v", changes, err)
	}
	after := writeTree(t, map[string]string{"api/db/kit/embed.go": "package kit\n", "api/internal/handlers/example_test.go": string(data)})
	original := writeTree(t, map[string]string{"api/db/kit/embed.go": "package kit\n", "api/internal/handlers/example_test.go": oldKitTest})
	migratetest.Run(t, m, original, after, migratetest.WithRunner(execx.OSRunner{}))
}

func TestKitTestkitSkippedCalls(t *testing.T) {
	t.Parallel()
	for _, call := range []string{
		`testkit.DBWithMigrations(tb, migrations.FS)`, `testkit.DBWithMigrations(t, custom.FS)`,
		`other.DBWithMigrations(t, migrations.FS)`, `testkit.DBWithMigrations(t, migrations.FS, option)`,
		`"testkit.DBWithMigrations(t, migrations.FS)"`, `testkit.DB(t)`,
	} {
		t.Run(call, func(t *testing.T) {
			source := "package test\nfunc test() { _ = " + call + " }\n"
			root := writeTree(t, map[string]string{"api/db/kit/embed.go": "package kit\n", "api/test_test.go": source, "web/example_test.go": oldKitTest, "api/example.go": oldKitTest})
			m := kitMigration(t, "0004_kit_testkit")
			migratetest.Run(t, m, root, root)
		})
	}
	root := writeTree(t, map[string]string{"api/example_test.go": oldKitTest})
	migratetest.Run(t, kitMigration(t, "0004_kit_testkit"), root, root)
}

type badKitFormatter struct{}

func (badKitFormatter) Run(_ context.Context, cmd execx.Cmd) (execx.Result, error) {
	if cmd.Name != "gofmt" {
		panic("unexpected command")
	}
	return execx.Result{}, errors.New("format failed")
}

func TestKitTestkitManualStep(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source string
		runner       execx.Runner
	}{
		{"parse", oldKitTest + "func broken(\n", execx.OSRunner{}},
		{"format", oldKitTest, badKitFormatter{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"api/db/kit/embed.go": "package kit\n", "api/example_test.go": tc.source})
			m := kitMigration(t, "0004_kit_testkit")
			c := &migrate.Ctx{Root: root, Runner: tc.runner}
			changes, err := m.Plan(c)
			if err == nil || len(changes) != 1 || !strings.HasPrefix(changes[0].Desc, "Manual step:") || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("changes = %+v, error = %v", changes, err)
			}
			if err := m.Apply(c); err == nil {
				t.Fatal("applied unformattable file")
			}
			data, _ := os.ReadFile(filepath.Join(root, "api/example_test.go"))
			if string(data) != tc.source {
				t.Fatal("changed unformattable file")
			}
		})
	}
}

func TestKitTestkitExistingImport(t *testing.T) {
	t.Parallel()
	for _, importLine := range []string{`kitmigrations "example.com/demo/api/db/kit"`, `customkit "example.com/demo/api/db/kit"`, `"example.com/demo/api/db/kit"`} {
		t.Run(importLine, func(t *testing.T) {
			source := strings.Replace(oldKitTest, `"testing"`, `"testing"`+"\n"+importLine, 1)
			root := writeTree(t, map[string]string{"api/db/kit/embed.go": "package kit\n", "api/example_test.go": source})
			m := kitMigration(t, "0004_kit_testkit")
			if err := m.Apply(&migrate.Ctx{Root: root, Runner: execx.OSRunner{}}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, "api/example_test.go"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Count(data, []byte(`"example.com/demo/api/db/kit"`)) != 1 {
				t.Fatalf("duplicate kit import: %s", data)
			}
			alias := strings.Fields(importLine)[0]
			if alias[0] == '"' {
				alias = "kit"
			}
			if !bytes.Contains(data, []byte("testkit.WithKitMigrations("+alias+".FS)")) {
				t.Fatalf("wrong kit alias: %s", data)
			}
		})
	}
}

func TestKitTestkitInvalidUnrelatedFile(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"api/db/kit/embed.go": "package kit\n", "api/example_test.go": `package test
var example = "testkit.DBWithMigrations(t, migrations.FS)"
func broken(
`})
	migratetest.Run(t, kitMigration(t, "0004_kit_testkit"), root, root)
}

func TestKitTestkitMultilineCall(t *testing.T) {
	t.Parallel()
	source := strings.Replace(oldKitTest, "testkit.DBWithMigrations(t, migrations.FS)", "testkit.DBWithMigrations(\n t,\n migrations.FS, // The app migrations.\n )", 2)
	root := writeTree(t, map[string]string{"api/db/kit/embed.go": "package kit\n", "api/example_test.go": source})
	if err := kitMigration(t, "0004_kit_testkit").Apply(&migrate.Ctx{Root: root, Runner: execx.OSRunner{}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "api/example_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte("testkit.WithKitMigrations(kitmigrations.FS)")) != 2 || !bytes.Contains(data, []byte("// The app migrations.")) {
		t.Fatalf("wrong calls: %s", data)
	}
}
