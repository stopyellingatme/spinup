package handler

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
)

func TestHandler(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "go.mod"), []byte("module example.com/todo/api\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(context.Background(), gen.Input{Root: root, Args: []string{"HealthReport"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"api/internal/handlers/health_report.go", "api/internal/handlers/health_report_test.go"}
	if got := out.Plan.Paths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths %v, want %v", got, want)
	}
	src, _ := out.Plan.Get(want[0])
	if !strings.Contains(string(src.Data), "func HealthReport(w http.ResponseWriter, r *http.Request) {") {
		t.Fatalf("handler:\n%s", src.Data)
	}
	test, _ := out.Plan.Get(want[1])
	if !strings.Contains(string(test.Data), `"example.com/todo/api/internal/handlers"`) {
		t.Fatalf("test:\n%s", test.Data)
	}
	if !strings.Contains(out.Next[0], `mux.HandleFunc("GET /api/health_report", handlers.HealthReport)`) {
		t.Fatalf("next %v", out.Next)
	}

	for _, args := range [][]string{nil, {"9lives"}, {"main"}} {
		if _, err := run(context.Background(), gen.Input{Root: root, Args: args}); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("%v: error %v, want one with a Try: line", args, err)
		}
	}
}
