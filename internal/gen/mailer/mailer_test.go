package mailer

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestPlan(t *testing.T) {
	t.Parallel()
	g, ok := registry.Lookup("mailer")
	if !ok {
		t.Fatal("mailer is not registered")
	}
	for _, tc := range []struct {
		name        string
		packs, args []string
		want        string
	}{
		{"welcome", []string{"api", "mail"}, []string{"welcome", "name:string"}, ""},
		{"typed", []string{"api", "mail"}, []string{"receipt", "sent_at:time", "due:date?", "key:uuid", "price:decimal", "count:int", "payload:json"}, ""},
		{"no mail", []string{"api"}, []string{"welcome"}, "Try: spinup module add mail"},
		{"no name", []string{"mail"}, nil, "expected a mailer name"},
		{"path", []string{"mail"}, []string{"../escape"}, "invalid mailer name"},
		{"reserved", []string{"mail"}, []string{"mailers"}, "reserved"},
		{"Go field collision", []string{"mail"}, []string{"welcome", "send_2fa:string", "send2fa:int"}, "Go field Send2fa is duplicated"},
		{"duplicate", []string{"mail"}, []string{"welcome", "name:string", "name:int"}, "duplicated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := gen.Input{Root: t.TempDir(), Lock: &manifest.Lock{Packs: tc.packs}, Args: tc.args}
			out, err := g.Run(t.Context(), in)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Plan.Files) != 4 {
				t.Fatalf("files = %d", len(out.Plan.Files))
			}
			again, err := g.Run(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			for i, f := range out.Plan.Files {
				if !bytes.Equal(f.Data, again.Plan.Files[i].Data) {
					t.Fatal("non-deterministic output")
				}
				if _, err := os.Stat(filepath.Join(in.Root, f.Path)); !os.IsNotExist(err) {
					t.Fatalf("generator wrote %s", f.Path)
				}
				if strings.HasSuffix(f.Path, ".go") {
					if f.Class != "managed" {
						t.Fatalf("Go class: %s", f.Class)
					}
					if _, err := parser.ParseFile(token.NewFileSet(), f.Path, f.Data, parser.AllErrors); err != nil {
						t.Fatal(err)
					}
				} else if f.Class != "seed" {
					t.Fatalf("template class: %s", f.Class)
				}
			}
		})
	}
}
