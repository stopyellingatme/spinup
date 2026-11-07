package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	s := newServer(t, Config{})
	type input struct {
		Limit int `json:"limit" jsonschema:"The page size."`
	}
	Tool(s, ToolDef{Name: "z_write", Title: "Write", Description: "Write a value", Roles: []string{"manager"}}, func(context.Context, input) (testOutput, error) {
		t.Error("command executed handler")
		return testOutput{}, nil
	})
	Tool(s, ToolDef{Name: "a_read", Description: "Read a value", ReadOnly: true}, func(context.Context, input) (testOutput, error) {
		t.Error("command executed handler")
		return testOutput{}, nil
	})
	var out bytes.Buffer
	if err := s.runCommand(t.Context(), []string{"tools"}, &out); err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Name, Title, Description  string
		Roles                     []string
		Annotations               map[string]any
		InputSchema, OutputSchema map[string]any
	}
	if err := json.Unmarshal(out.Bytes(), &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "a_read" || tools[1].Name != "z_write" || tools[1].Title != "Write" || tools[1].Description != "Write a value" || len(tools[1].Roles) != 1 || tools[1].Roles[0] != "manager" {
		t.Fatalf("command: %s", out.String())
	}
	if tools[0].InputSchema["properties"] == nil || tools[0].OutputSchema["properties"] == nil || tools[0].Annotations["readOnlyHint"] != true {
		t.Fatalf("schemas: %s", out.String())
	}
	var again bytes.Buffer
	if err := s.runCommand(t.Context(), []string{"tools"}, &again); err != nil {
		t.Fatal(err)
	}
	if out.String() != again.String() {
		t.Fatal("nondeterministic command output")
	}
	for _, args := range [][]string{nil, {"bad"}, {"tools", "extra"}, {"--help"}} {
		out.Reset()
		if err := s.runCommand(t.Context(), args, &out); err == nil || !strings.Contains(err.Error(), "\nTry: mcp tools") || out.Len() != 0 {
			t.Fatalf("args %v: %v %s", args, err, out.String())
		}
	}
	if err := s.Command()(t.Context(), []string{"bad"}); err == nil {
		t.Fatal("Command accepted invalid args")
	}
	// Exercise the public stdout adapter without a pipe or a listener.
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	old := os.Stdout
	os.Stdout = f
	err = s.Command()(t.Context(), []string{"tools"})
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil || !bytes.Equal(data, again.Bytes()) {
		t.Fatalf("stdout: %s %v", data, err)
	}
	if err = s.runCommand(t.Context(), []string{"tools"}, brokenWriter{}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("write failure: %v", err)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken output") }

func TestCommandEmpty(t *testing.T) {
	s := newServer(t, Config{})
	var out bytes.Buffer
	if err := s.runCommand(t.Context(), []string{"tools"}, &out); err != nil || out.String() != "[]\n" {
		t.Fatalf("empty list: %q %v", out.String(), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.runCommand(ctx, []string{"tools"}, &out); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("cancelled command: %v", err)
	}
}
