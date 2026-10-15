package pack

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	execx "github.com/stopyellingatme/spinup/internal/exec"
)

// Git needs space for its object database and the checkout as well as metadata.
const maxFetchBytes = 4 * maxBytes
const maxFetchEntries = 4*maxFiles + 256

func checkFetchSize(dir string) error {
	var size int64
	entries := 0
	return filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			// Git atomically renames and removes temporary files during a fetch.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		entries++
		if entries > maxFetchEntries {
			return fmt.Errorf("plugin fetch exceeds %d entries", maxFetchEntries)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		size += info.Size()
		if size > maxFetchBytes {
			return fmt.Errorf("plugin fetch exceeds %d bytes", maxFetchBytes)
		}
		return nil
	})
}

func runFetchGit(ctx context.Context, runner execx.Runner, cmd execx.Cmd) (execx.Result, error) {
	if err := checkFetchSize(cmd.Dir); err != nil {
		return execx.Result{}, problem("limit plugin fetch", err)
	}
	limited, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-limited.Done():
				return
			case <-ticker.C:
				if err := checkFetchSize(cmd.Dir); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	result, err := runner.Run(limited, cmd)
	close(stop)
	<-done
	if cause := context.Cause(limited); cause != nil {
		return result, problem("limit plugin fetch", cause)
	}
	if limitErr := checkFetchSize(cmd.Dir); limitErr != nil {
		return result, problem("limit plugin fetch", limitErr)
	}
	return result, err
}
