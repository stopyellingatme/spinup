package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildImagesLocalKitContext(t *testing.T) {
	t.Parallel()
	root := imageProject(t, "module github.com/acme/todo/api\nreplace github.com/stopyellingatme/spinup/kit => ../kit\n")
	kit := filepath.Join(root, "kit")
	if err := os.Mkdir(kit, 0755); err != nil {
		t.Fatal(err)
	}
	runner := &metadataRunner{}
	if _, err := BuildImages(context.Background(), runner, ImageOptions{Root: root, App: "todo", Tag: "v1"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %#v", runner.calls)
	}
	api, web := strings.Join(runner.calls[0].Args, " "), strings.Join(runner.calls[1].Args, " ")
	if !strings.Contains(api, "--build-context kit="+kit+" ") {
		t.Fatalf("api args = %s", api)
	}
	if strings.Contains(web, "kit=") {
		t.Fatalf("web args = %s", web)
	}
}

func TestBuildImagesNoKitContextWithoutReplace(t *testing.T) {
	t.Parallel()
	root := imageProject(t, "module github.com/acme/todo/api\n")
	runner := &metadataRunner{}
	if _, err := BuildImages(context.Background(), runner, ImageOptions{Root: root, App: "todo", Tag: "v1"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range runner.calls {
		if strings.Contains(strings.Join(c.Args, " "), "kit=") {
			t.Fatalf("args = %v", c.Args)
		}
	}
}

func TestBuildImagesMissingKitDirectory(t *testing.T) {
	t.Parallel()
	root := imageProject(t, "module github.com/acme/todo/api\nreplace github.com/stopyellingatme/spinup/kit => ../kit\n")
	runner := &metadataRunner{}
	_, err := BuildImages(context.Background(), runner, ImageOptions{Root: root, App: "todo", Tag: "v1"})
	if err == nil || !strings.Contains(errorText(err), "Try:") {
		t.Fatalf("error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %#v", runner.calls)
	}
}
