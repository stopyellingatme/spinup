package render

import (
	"testing"

	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func BenchmarkRenderAPI(b *testing.B) {
	pack, err := templates.Load(templates.Embedded(), "api")
	if err != nil {
		b.Fatal(err)
	}
	in := Input{Packs: []*templates.Pack{pack}, Answers: map[string]any{"app_name": "example", "module": "example.com/example", "modules": []string{"ci", "deploy"}, "db_version": "18"}, Stack: stackversions.MustLoad(), TemplateVersion: "0.1.0"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(in); err != nil {
			b.Fatal(err)
		}
	}
}
