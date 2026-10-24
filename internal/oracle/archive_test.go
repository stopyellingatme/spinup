package oracle

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
	"github.com/stopyellingatme/spinup/internal/templates/store"
)

type archiveEntry struct {
	Name string `json:"name"`
	Mode int64  `json:"mode"`
	Type byte   `json:"type"`
	Data string `json:"data"`
}

func archiveEntries(t *testing.T, data []byte) []archiveEntry {
	t.Helper()
	zr, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	reader := tar.NewReader(zr)
	var entries []archiveEntry
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.ModTime.Unix() != 0 || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
			t.Fatalf("unstable tar metadata: %#v", h)
		}
		entries = append(entries, archiveEntry{h.Name, h.Mode, h.Typeflag, base64.StdEncoding.EncodeToString(content)})
	}
	return entries
}

func generate(t *testing.T) map[string][]byte {
	t.Helper()
	fixtures := map[string][]byte{
		"locks.json":        jsonBytes(t, lockCases(t)),
		"diffs.json":        jsonBytes(t, diffCases(t)),
		"quotes.json":       jsonBytes(t, quoteCases()),
		"plan-paths.json":   jsonBytes(t, planCases()),
		"paths.json":        jsonBytes(t, pathCases()),
		"rooted-paths.json": jsonBytes(t, rootedCases(t)),
	}
	packs := fstest.MapFS{
		"base/.gitignore":      {Data: []byte("secret\n"), Mode: 0o644},
		"base/run.sh":          {Data: []byte("#!/bin/sh\necho '<>&'\n"), Mode: 0o755},
		"base/empty":           {Data: []byte{}, Mode: 0o600},
		"base/nested/data.bin": {Data: []byte{0, 255, 1, 128}, Mode: 0o644},
	}
	stack, err := os.ReadFile("../stackversions/stackversions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := store.WriteArchive(&archive, packs, stack, []byte("{\"schema\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadArchive(archive.Bytes(), "1.2.3"); err != nil {
		t.Fatal(err)
	}
	// This public, fixed seed is for tests only. Never use a release key here.
	seed := sha256.Sum256([]byte("spinup R0.5 PUBLIC TEST KEY. NEVER USE FOR RELEASES."))
	private := ed25519.NewKeyFromSeed(seed[:])
	fixtures["templates-v1.2.3.tar.zst"] = archive.Bytes()
	fixtures["templates-v1.2.3.tar.zst.sig"] = ed25519.Sign(private, archive.Bytes())
	fixtures["test-key.pub"] = private.Public().(ed25519.PublicKey)
	fixtures["archive-entries.json"] = jsonBytes(t, archiveEntries(t, archive.Bytes()))
	return fixtures
}

func checkRustArchive(t *testing.T, filename string, expected []byte) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadArchive(data, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(archiveEntries(t, data))
	if err != nil {
		t.Fatal(err)
	}
	var entries []archiveEntry
	if err := json.Unmarshal(expected, &entries); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, want) {
		t.Fatal("Rust archive differs from Go decoded entries")
	}
}
