package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen/newapp"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestNewQuestionFlags(t *testing.T) {
	t.Parallel()
	command := newNew(&App{Flags: &GlobalFlags{}, UI: ui.New(nil, nil, nil, ui.Options{})})
	packs, err := newapp.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	questions, err := newapp.Questions(packs)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range questions {
		name := strings.ReplaceAll(q.Key, "_", "-")
		flag := command.Flags().Lookup(name)
		if q.Key == "app_name" {
			if flag != nil {
				t.Fatal("app name flag exists")
			}
			continue
		}
		if flag == nil {
			t.Errorf("missing flag %s", name)
		}
	}
}

func TestNewJSONAndDryRun(t *testing.T) {
	t.Parallel()
	for _, dry := range []bool{false, true} {
		t.Run(map[bool]string{true: "dry", false: "apply"}[dry], func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var out, stderr bytes.Buffer
			args := []string{"new", "todo", "--dir", dir, "--api-only", "--module", "example.com/todo", "--no-git", "--non-interactive", "--json"}
			if dry {
				args = append(args, "--dry-run")
			}
			code := Execute(context.Background(), args, nil, &out, &stderr)
			if code != 0 {
				t.Fatalf("exit=%d: %s", code, stderr.String())
			}
			var got struct {
				Dir   string
				Packs []string
				Files int
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Dir != filepath.Join(dir, "todo") || len(got.Packs) != 3 || got.Files == 0 {
				t.Fatalf("JSON=%s", out.String())
			}
			_, err := os.Stat(got.Dir)
			if dry && !os.IsNotExist(err) {
				t.Fatal("dry-run created directory")
			}
			if !dry && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDBHelpAndGuards(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"db"}, {"db", "migrate", "down"}, {"db", "migrate", "invalid"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var out, stderr bytes.Buffer
			code := Execute(context.Background(), args, nil, &out, &stderr)
			if len(args) == 1 {
				if code != 0 || !strings.Contains(out.String(), "migrate") {
					t.Fatalf("exit=%d, out=%s", code, out.String())
				}
				return
			}
			if code == 0 || !strings.Contains(stderr.String(), "Try:") {
				t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
			}
		})
	}
}
