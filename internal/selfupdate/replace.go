package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const binaryLimit = 256 << 20

// Only copy the expected regular binary. Never extract archive paths to disk.
func extract(data []byte, goos string) ([]byte, error) {
	name := "spinup"
	var binary []byte
	read := func(r io.Reader) error {
		if binary != nil {
			return failure("the archive contains duplicate binaries")
		}
		b, err := io.ReadAll(io.LimitReader(r, binaryLimit+1))
		if err != nil {
			return failure(err)
		}
		if len(b) == 0 || len(b) > binaryLimit {
			return failure("the archive binary is empty or too large")
		}
		binary = b
		return nil
	}
	if goos == "windows" {
		name += ".exe"
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, failure(err)
		}
		for _, f := range zr.File {
			if f.Name != name {
				continue
			}
			if !f.Mode().IsRegular() {
				return nil, failure("the archive binary is not a regular file")
			}
			r, e := f.Open()
			if e != nil {
				return nil, failure(e)
			}
			e = read(r)
			_ = r.Close()
			if e != nil {
				return nil, e
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, failure(err)
		}
		defer gz.Close()
		tr := tar.NewReader(io.LimitReader(gz, 512<<20))
		for {
			h, e := tr.Next()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				return nil, failure(e)
			}
			if h.Name != name {
				continue
			}
			if h.Typeflag != tar.TypeReg {
				return nil, failure("the archive binary is not a regular file")
			}
			if e = read(tr); e != nil {
				return nil, e
			}
		}
	}
	if binary == nil {
		return nil, failure("the archive has no regular binary")
	}
	return binary, nil
}

func replace(path string, binary []byte, goos string) error {
	dir := filepath.Dir(path)
	permissionError := func(err error) error {
		return fmt.Errorf("cannot replace spinup: the executable directory is not writable: %w\nTry: re-run with permissions or reinstall", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return permissionError(err)
	}
	if info.Mode().Perm()&0222 == 0 {
		return permissionError(os.ErrPermission)
	}
	f, err := os.CreateTemp(dir, ".spinup-*")
	if err != nil {
		return permissionError(err)
	}
	staged := f.Name()
	defer os.Remove(staged)
	if _, err = f.Write(binary); err == nil {
		err = f.Chmod(0755)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return failure(err)
	}
	if goos == "windows" {
		old := path + ".old"
		if err = os.Rename(path, old); err != nil {
			return failure(err)
		}
		if err = os.Rename(staged, path); err != nil {
			rollbackErr := os.Rename(old, path)
			if rollbackErr != nil {
				return failure(fmt.Sprintf("replacement failed: %v; restore %s manually: %v", err, old, rollbackErr))
			}
			return failure(err)
		}
		return nil
	}
	if err = os.Rename(staged, path); err != nil {
		return permissionError(err)
	}
	return nil
}
