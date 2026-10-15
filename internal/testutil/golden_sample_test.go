package testutil

import (
	"io/fs"
	"testing"
)

func TestGolden_sample(t *testing.T) {
	if !GoldenSelected(t, "_sample") {
		return
	}
	answers := map[string]any{"app_name": "demo", "db": "postgres", "auth": true, "modules": []string{"jobs", "mail"}, "greeting": "Hello"}
	render := func() (fs.FS, error) { return RenderPack(t, []string{"_sample"}, answers), nil }
	got, err := render()
	if err != nil {
		t.Fatal(err)
	}
	Golden(t, "_sample", got)
	Deterministic(t, render)
}
