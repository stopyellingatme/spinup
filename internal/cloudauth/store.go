package cloudauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Store is a token file with a lock file next to it.
type Store struct {
	Dir  string
	Name string
}

// DefaultDir returns SPINUP_CREDENTIALS_DIR, or ~/.config/spinup/credentials.
func DefaultDir() (string, error) {
	if d := os.Getenv("SPINUP_CREDENTIALS_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", problem("resolve the credentials directory", "the home directory is unavailable", "set SPINUP_CREDENTIALS_DIR")
	}
	return filepath.Join(home, ".config", "spinup", "credentials"), nil
}

func (s Store) path() string     { return filepath.Join(s.Dir, s.Name) }
func (s Store) lockPath() string { return s.path() + ".lock" }

// Load reads the token. A missing file returns found false and no error.
// A file that group or other users can access is an error (not checked on Windows).
func (s Store) Load() (Token, bool, error) {
	p := s.path()
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return Token{}, false, nil
	}
	if err != nil {
		return Token{}, false, problem("read the stored sign-in", err.Error(), "check the file "+p)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return Token{}, false, problem("read the stored sign-in",
			fmt.Sprintf("the file %s allows access to other users (mode %04o)", p, fi.Mode().Perm()), "chmod 600 "+p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Token{}, false, problem("read the stored sign-in", err.Error(), "check the file "+p)
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return Token{}, false, problem("read the stored sign-in", "the file is not valid JSON", "delete "+p+" and sign in again")
	}
	return t, true, nil
}

// Save writes the token with a temporary file and a rename. The directory gets mode 0700 and the file 0600.
func (s Store) Save(t Token) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return problem("save the sign-in", err.Error(), "check that "+s.Dir+" is writable")
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil && runtime.GOOS != "windows" {
		return problem("save the sign-in", err.Error(), "check the permissions of "+s.Dir)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return problem("save the sign-in", err.Error(), "")
	}
	tmp, err := os.CreateTemp(s.Dir, s.Name+".tmp*")
	if err != nil {
		return problem("save the sign-in", err.Error(), "check that "+s.Dir+" is writable")
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return problem("save the sign-in", err.Error(), "check the free disk space and the permissions of "+s.Dir)
	}
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return problem("save the sign-in", err.Error(), "")
	}
	if err := os.Rename(name, s.path()); err != nil {
		_ = os.Remove(name)
		return problem("save the sign-in", err.Error(), "check the permissions of "+s.Dir)
	}
	return nil
}

// Delete removes the token file. A missing file is not an error.
func (s Store) Delete() error {
	if err := os.Remove(s.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return problem("delete the stored sign-in", err.Error(), "delete "+s.path()+" yourself")
	}
	return nil
}

// WithLock runs fn while it holds an exclusive lock on the lock file.
// It polls every 100 ms until the lock is free or ctx ends.
func (s Store) WithLock(ctx context.Context, fn func() error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return problem("lock the credentials", err.Error(), "check that "+s.Dir+" is writable")
	}
	unlock, err := acquire(ctx, s.lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
