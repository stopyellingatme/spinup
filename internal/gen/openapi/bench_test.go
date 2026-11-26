package openapi

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkBundleTwentyResources(b *testing.B) {
	dir := b.TempDir()
	paths := filepath.Join(dir, "openapi", "paths")
	if err := os.MkdirAll(paths, 0755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi", "openapi.yaml"), []byte("openapi: 3.0.0\ninfo:\n  title: Test\n  version: 1.0.0\npaths: {}\n"), 0644); err != nil {
		b.Fatal(err)
	}
	for i := range 20 {
		data := fmt.Appendf(nil, "/resource%d:\n  get:\n    responses:\n      '200':\n        description: OK\n", i)
		if err := os.WriteFile(filepath.Join(paths, fmt.Sprintf("%02d.yaml", i)), data, 0644); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for b.Loop() {
		_, count, err := Bundle(dir)
		if err != nil {
			b.Fatal(err)
		}
		if count != 20 {
			b.Fatal(count)
		}
	}
}
