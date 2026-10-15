package store

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/templates"
)

const maxArchive = maxUnpacked + maxEntries*1024

func archiveName(version string) string { return "templates-v" + version + ".tar.zst" }

func readFS(source fs.FS, name string, limit int64) ([]byte, error) {
	file, err := source.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return readLimited(file, limit)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("template data exceeds the size limit")
	}
	return data, nil
}

func (s *Store) verify(data, sig []byte) error {
	if len(s.options.Keys) == 0 {
		return errors.New("no trusted template signing keys are configured in this build")
	}
	for _, key := range s.options.Keys {
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, data, sig) {
			return nil
		}
	}
	return errors.New("template archive signature is invalid")
}

func (s *Store) download(ctx context.Context, version string) (*templates.Version, error) {
	if len(s.options.Keys) == 0 {
		return nil, errors.New("no trusted template signing keys are configured in this build")
	}
	url := strings.TrimRight(s.options.BaseURL, "/") + "/v" + version + "/" + archiveName(version)
	data, err := s.get(ctx, url, maxArchive)
	if err != nil {
		return nil, err
	}
	sig, err := s.get(ctx, url+".sig", ed25519.SignatureSize)
	if err != nil {
		return nil, err
	}
	if err := s.verify(data, sig); err != nil {
		return nil, err
	}
	v, err := ReadArchive(data, version)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.options.CacheDir, 0755); err != nil {
		return nil, err
	}
	name := filepath.Join(s.options.CacheDir, archiveName(version))
	// Publish the signature first. A concurrent load rejects any incomplete pair.
	if err := writeAtomic(name+".sig", sig); err != nil {
		return nil, err
	}
	if err := writeAtomic(name, data); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Store) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.options.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, response.StatusCode)
	}
	return readLimited(response.Body, limit)
}

func writeAtomic(name string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(name), ".templates-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), name)
}
