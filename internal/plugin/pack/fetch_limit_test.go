package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchSizeLimits(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"within-limit", "bytes", "entries"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if kind == "entries" {
				for i := 0; i < maxFetchEntries; i++ {
					if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%04d", i)), nil, 0644); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				file, err := os.Create(filepath.Join(root, "pack"))
				if err != nil {
					t.Fatal(err)
				}
				size := int64(maxFetchBytes)
				if kind == "bytes" {
					size++
				}
				truncateErr := file.Truncate(size)
				closeErr := file.Close()
				if truncateErr != nil {
					t.Fatal(truncateErr)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			err := checkFetchSize(root)
			if kind == "within-limit" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "fetch exceeds") {
				t.Fatalf("limit=%v", err)
			}
		})
	}
}
