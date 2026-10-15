package routes

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List(root)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	write("api/openapi/bundle.gen.yaml", "paths:\n  /api/todos:\n    post:\n      operationId: createTodo\n    get:\n      operationId: listTodos\n    parameters: []\n")
	write("api/internal/routes/custom.go", "package routes\nconst base = \"/custom\"\nfunc register() {\n mux.HandleFunc(\"POST \" + base, nil)\n mux.Handle(base, nil)\n mux.HandleFunc(dynamic, nil)\n other.HandleFunc(\"GET /ignored\", nil)\n}\n")
	write("api/internal/routes/custom_test.go", "not Go")
	got, err = List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 || got[0].Source != "openapi:listTodos" || got[3].Method != http.MethodPost || got[3].Source != "custom.go:4" {
		t.Fatalf("%+v", got)
	}
	write("api/openapi/bundle.gen.yaml", "paths: [bad")
	if _, err = List(root); err == nil {
		t.Fatal("accepted invalid YAML")
	}
}

func TestConstantsAcrossFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "api/internal/routes")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"constants.go": "package routes\nconst routePattern = `GET /cross-file`\n",
		"routes.go":    "package routes\nfunc register(){mux.HandleFunc(routePattern, nil)}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Path != "/cross-file" || got[0].Source != "routes.go:2" {
		t.Fatalf("%+v", got)
	}
}
