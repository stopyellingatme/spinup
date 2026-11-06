package vfs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func benchmarkPlan(b *testing.B) (string, Plan) {
	b.Helper()
	root := b.TempDir()
	var plan Plan
	for i := range 40 {
		name := fmt.Sprintf("file%02d.go", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("package example\n\nvar value = 1\n"), 0644); err != nil {
			b.Fatal(err)
		}
		if err := plan.Add(File{Path: name, Data: []byte("package example\n\nvar value = 2\n"), Mode: 0644}); err != nil {
			b.Fatal(err)
		}
	}
	return root, plan
}

func BenchmarkDiff40(b *testing.B) {
	root, plan := benchmarkPlan(b)
	b.ReportAllocs()
	for b.Loop() {
		changes, err := Diff(root, plan)
		if err != nil {
			b.Fatal(err)
		}
		if len(changes) != 40 {
			b.Fatal("unexpected change count")
		}
	}
}

func BenchmarkApply40(b *testing.B) {
	root, plan := benchmarkPlan(b)
	b.ReportAllocs()
	for b.Loop() {
		changes, err := Apply(root, plan, ApplyOptions{})
		if err != nil {
			b.Fatal(err)
		}
		if len(changes) != 40 {
			b.Fatal("unexpected change count")
		}
		b.StopTimer()
		for _, f := range plan.Files {
			if err := os.WriteFile(filepath.Join(root, f.Path), []byte("package example\n\nvar value = 1\n"), 0644); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
	}
}
