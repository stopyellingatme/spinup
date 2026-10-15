package modules

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates"
)

type questionPrompt struct{ calls int }

func (p *questionPrompt) Ask(_ context.Context, _ templates.Question, value any) (any, error) {
	p.calls++
	return value, nil
}

func TestSelect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		modules                 []string
		names                   []string
		flags                   map[string]any
		web, interactive        bool
		wantAdded, wantRequired []string
		wantError               string
	}{
		{name: "dependency closure", names: []string{"monitoring"}, wantAdded: []string{"deploy", "monitoring"}, wantRequired: []string{"deploy"}},
		{name: "existing dependency", modules: []string{"deploy"}, names: []string{"monitoring"}, flags: map[string]any{"monitoring_profile": "full"}, wantAdded: []string{"monitoring"}},
		{name: "duplicate request", names: []string{"jobs", "jobs"}, wantAdded: []string{"jobs"}},
		{name: "already enabled", modules: []string{"jobs"}, names: []string{"jobs"}},
		{name: "unknown", names: []string{"bogus"}, wantError: "Try: spinup module list"},
		{name: "core", names: []string{"web"}, wantError: "unknown module"},
		{name: "web required", names: []string{"auth"}, wantError: "API-only"},
		{name: "web available", names: []string{"auth"}, web: true, wantAdded: []string{"auth"}},
		{name: "invalid profile", names: []string{"monitoring"}, flags: map[string]any{"monitoring_profile": "wrong"}, wantError: "--monitoring-profile"},
		{name: "unselected flag", names: []string{"jobs"}, flags: map[string]any{"monitoring_profile": "full"}, wantError: "does not belong"},
		{name: "interactive default", names: []string{"monitoring"}, interactive: true, wantAdded: []string{"deploy", "monitoring"}, wantRequired: []string{"deploy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &project.Config{Modules: slices.Clone(tc.modules)}
			lock := manifest.New()
			lock.Packs = append([]string{"base", "api"}, tc.modules...)
			if tc.web {
				lock.Packs = append(lock.Packs, "web")
			}
			lock.Answers = map[string]any{"app_name": "demo", "module": "example.com/demo", "modules": slices.Clone(tc.modules), "db_version": "18"}
			original := slices.Clone(lock.Packs)
			prompt := &questionPrompt{}
			got, err := Select(context.Background(), SelectOptions{Config: cfg, Lock: lock, FS: templates.Embedded(), Names: tc.names, Answers: tc.flags, Interactive: tc.interactive, Prompter: prompt})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Added, tc.wantAdded) || !slices.Equal(got.Required, tc.wantRequired) {
				t.Fatalf("selection=%+v", got)
			}
			if !slices.Equal(original, lock.Packs) || !reflect.DeepEqual(cfg.Modules, tc.modules) {
				t.Fatal("mutated input")
			}
			if !tc.interactive && prompt.calls != 0 {
				t.Fatal("prompted without a terminal")
			}
			if slices.Contains(tc.names, "monitoring") {
				want := "lite"
				if tc.flags != nil {
					want = tc.flags["monitoring_profile"].(string)
				}
				if got.Answers["monitoring_profile"] != want {
					t.Fatalf("answers=%v", got.Answers)
				}
				if tc.interactive && prompt.calls != 1 {
					t.Fatalf("prompts=%d", prompt.calls)
				}
			}
		})
	}
}

func TestSelectLockedQuestions(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"base/pack.toml": {Data: []byte("name='base'\n")},
		"api/pack.toml":  {Data: []byte("name='api'\n")},
		"jobs/pack.toml": {Data: []byte("name='jobs'\nrequires=['api']\n[[questions]]\nkey='queue_name'\nprompt='Queue name'\ntype='string'\n")},
	}
	lock := manifest.New()
	lock.Packs = []string{"base", "api"}
	for _, flags := range []map[string]any{nil, {"queue_name": "priority"}} {
		got, err := Select(context.Background(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: fsys, Names: []string{"jobs"}, Answers: flags})
		if flags == nil {
			if err == nil || !strings.Contains(err.Error(), "--queue-name") {
				t.Fatalf("error=%v", err)
			}
		} else if err != nil || got.Answers["queue_name"] != "priority" {
			t.Fatalf("selection=%v error=%v", got, err)
		}
	}
}
