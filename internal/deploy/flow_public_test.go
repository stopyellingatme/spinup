package deploy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestReleaseFilesPublicRoutes(t *testing.T) {
	t.Parallel()
	service := ocrService
	service.Public = "/svc/ocr"
	f := &flow{
		l: &local{
			app:      "todo",
			sidecars: []services.Service{service},
			envFile:  EnvFile{Replicas: map[string]int{"ocr": 1}},
		},
		images: []Image{{Service: "ocr", Ref: "todo-ocr:test"}},
	}

	files, err := f.releaseFiles()
	if err != nil {
		t.Fatal(err)
	}
	want, err := PublicRoutes([]services.Service{service})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Path == PublicRoutesFile {
			if !bytes.Equal(file.Data, want) {
				t.Fatalf("public routes = %q, want %q", file.Data, want)
			}
			return
		}
	}
	t.Fatalf("release files do not include %s", PublicRoutesFile)
}

func TestReleaseFilesWithoutPublicRoutes(t *testing.T) {
	t.Parallel()
	f := &flow{l: &local{app: "todo"}}
	files, err := f.releaseFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Path == PublicRoutesFile {
			t.Fatalf("release files include %s", PublicRoutesFile)
		}
	}
}

func TestReleaseFilesPublicRoutesKeepPriorReleaseBytes(t *testing.T) {
	t.Parallel()
	service := ocrService
	service.Public = "/svc/ocr"
	first := publicRouteFile(t, service)

	service.Public = "/public/ocr"
	second := publicRouteFile(t, service)
	if bytes.Equal(first, second) {
		t.Fatal("changed public prefix did not change the route file")
	}
	if !bytes.Contains(first, []byte("/svc/ocr")) || bytes.Contains(first, []byte("/public/ocr")) {
		t.Fatalf("first route file = %q", first)
	}
}

func publicRouteFile(t *testing.T, service services.Service) []byte {
	t.Helper()
	f := &flow{
		l: &local{
			app:      "todo",
			sidecars: []services.Service{service},
			envFile:  EnvFile{Replicas: map[string]int{"ocr": 1}},
		},
		images: []Image{{Service: "ocr", Ref: "todo-ocr:test"}},
	}
	files, err := f.releaseFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Path == PublicRoutesFile {
			return bytes.Clone(file.Data)
		}
	}
	t.Fatalf("release files do not include %s", PublicRoutesFile)
	return nil
}

func TestPreflightPublicRoutesCollision(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	withService(t, x, false)
	writeTree(t, x.root, map[string]string{
		"spinup.toml":                strings.Replace(string(mustReadFile(t, x.root+"/spinup.toml")), "env = { MODEL = \"small\" }", "env = { MODEL = \"small\" }\npublic = \"/svc/ocr\"", 1),
		"deploy/" + PublicRoutesFile: "user route\n",
	})
	x.expectLocalGit()

	_, err := preflight(context.Background(), x.o, x.d, &x.out, false)
	var uiErr *ui.Error
	if err == nil || !errors.As(err, &uiErr) || !strings.Contains(uiErr.Why, "deploy/"+PublicRoutesFile) || uiErr.Try != "rename your file in deploy/Caddyfile.d" {
		t.Fatalf("preflight error = %v", err)
	}
	x.exec.AssertDone(t)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
