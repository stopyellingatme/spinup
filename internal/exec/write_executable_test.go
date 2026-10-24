package exec

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestWriteExecutableWritesAndRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := filepath.Join(dir, "tool"+string(rune('a'+i)))
			if err := WriteExecutable(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Error(err)
				return
			}
			if err := osexec.CommandContext(t.Context(), path).Run(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	info, err := os.Stat(filepath.Join(dir, "toola"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("mode = %v, err = %v", info, err)
	}
}
