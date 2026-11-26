package devloop

import (
	"fmt"
	"testing"
)

// BenchmarkWatcherPoll measures one poll of the watcher (a snapshot and the compare)
// over the inputs of a 20-resource app: 22 query files and 45 OpenAPI files.
func BenchmarkWatcherPoll(b *testing.B) {
	root := b.TempDir()
	for i := range 22 {
		writeInput(b, root, fmt.Sprintf("api/db/queries/q%02d.sql", i), "-- name: Q :one\nSELECT 1;\n")
	}
	for i := range 22 {
		writeInput(b, root, fmt.Sprintf("api/openapi/paths/p%02d.yaml", i), "get: {}\n")
		writeInput(b, root, fmt.Sprintf("api/openapi/schemas/s%02d.yaml", i), "S: {}\n")
	}
	writeInput(b, root, "api/openapi/openapi.yaml", "openapi: 3.1.0\n")
	writeInput(b, root, "api/openapi/bundle.gen.yaml", "generated: true\n")
	previous, err := snapshot(root, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		current, err := snapshot(root, nil)
		if err != nil {
			b.Fatal(err)
		}
		if steps := changes(previous, current, nil); len(steps) != 0 {
			b.Fatal(steps)
		}
		previous = current
	}
}
