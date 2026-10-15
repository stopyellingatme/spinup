package render

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestValidateAnswers(t *testing.T) {
	t.Parallel()
	p, err := templates.Load(templates.Embedded(), "_sample")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		answers map[string]any
		failure string
	}{
		{"defaults", nil, ""},
		{"provided", map[string]any{"app_name": "hello", "db": "none", "modules": []string{"jobs"}, "auth": true}, ""},
		{"comma modules", map[string]any{"modules": "jobs, mail"}, ""},
		{"json modules", map[string]any{"modules": []any{"jobs"}}, ""},
		{"bad string", map[string]any{"app_name": true}, "app_name"},
		{"bad pattern", map[string]any{"app_name": "Bad!"}, "--app-name"},
		{"bad bool", map[string]any{"auth": "false"}, "auth"},
		{"bad enum", map[string]any{"db": "sqlite"}, "db"},
		{"bad modules", map[string]any{"modules": []any{2}}, "modules"},
		{"reserved stack", map[string]any{"Stack": nil}, "reserved"},
		{"reserved version", map[string]any{"TemplateVersion": "x"}, "reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateAnswers([]*templates.Pack{p}, tc.answers)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("got %v, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "defaults" {
				want := map[string]any{"app_name": "demo", "db": "postgres", "modules": []string{}, "auth": false, "greeting": "Hello"}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %#v", got)
				}
			}
		})
	}
}

func TestMissingAnswer(t *testing.T) {
	t.Parallel()
	_, err := ValidateAnswers([]*templates.Pack{{Questions: []templates.Question{{Key: "app_name", Type: "string"}}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "--app-name") || !strings.Contains(err.Error(), "app_name") {
		t.Fatalf("error: %v", err)
	}
}

func TestAnswerDefaultsAndSharing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		packs   []*templates.Pack
		answers map[string]any
		failure string
	}{
		{name: "bool true", packs: []*templates.Pack{{Questions: []templates.Question{{Key: "auth", Type: "bool", Default: "true"}}}}},
		{name: "module default", packs: []*templates.Pack{{Questions: []templates.Question{{Key: "modules", Type: "string", Default: "jobs, mail"}}}}},
		{name: "enum default", packs: []*templates.Pack{{Questions: []templates.Question{{Key: "db", Type: "enum", Choices: []string{"none"}, Default: "postgres"}}}}, failure: "db"},
		{name: "pattern default", packs: []*templates.Pack{{Questions: []templates.Question{{Key: "app_name", Type: "string", Pattern: "^valid$", Default: "invalid"}}}}, failure: "app_name"},
		{name: "bad bool default", packs: []*templates.Pack{{Questions: []templates.Question{{Key: "auth", Type: "bool", Default: "invalid"}}}}, failure: "bool"},
		{name: "reserved question", packs: []*templates.Pack{{Questions: []templates.Question{{Key: "TemplateVersion", Type: "string"}}}}, failure: "reserved"},
		{name: "nil pack", packs: []*templates.Pack{nil}, failure: "nil"},
		{name: "shared", packs: []*templates.Pack{{Questions: []templates.Question{{Key: "db", Type: "string", Default: "none"}}}, {Questions: []templates.Question{{Key: "db", Type: "string", Default: "none"}}}}},
		{name: "conflicting", packs: []*templates.Pack{{Questions: []templates.Question{{Key: "db", Type: "string", Default: "none"}}}, {Questions: []templates.Question{{Key: "db", Type: "string", Default: "postgres"}}}}, failure: "conflicting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateAnswers(tc.packs, tc.answers)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("got %v, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "bool true" && got["auth"] != true {
				t.Fatalf("got %v", got)
			}
			if tc.name == "module default" && !reflect.DeepEqual(got["modules"], []string{"jobs", "mail"}) {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestAnswersRemainIndependent(t *testing.T) {
	t.Parallel()
	p := &templates.Pack{Questions: []templates.Question{{Key: "modules", Type: "string"}}}
	original := []string{"jobs"}
	answers := map[string]any{"modules": original}
	completed, err := ValidateAnswers([]*templates.Pack{p}, answers)
	if err != nil {
		t.Fatal(err)
	}
	completed["modules"].([]string)[0] = "mail"
	completed["new"] = "value"
	if original[0] != "jobs" || len(answers) != 1 {
		t.Fatal("input changed")
	}
}
