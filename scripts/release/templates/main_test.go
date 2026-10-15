package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/templates/store"
)

func TestReleaseArchive(t *testing.T) {
	t.Parallel()
	root := filepath.Clean("../../..")
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
	public := key.Public().(ed25519.PublicKey)
	for _, snapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "signed", true: "snapshot"}[snapshot], func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			opts := options{root: root, out: dir, version: "0.8.0-rc.1", snapshot: snapshot, trustedKeys: []ed25519.PublicKey{public}, signingKey: base64.StdEncoding.EncodeToString(key)}
			if err := generate(opts); err != nil {
				t.Fatal(err)
			}
			name := "templates-v0.8.0-rc.1.tar.zst"
			archive, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.ReadArchive(archive, opts.version)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := templates.Load(got.Packs, "base"); err != nil {
				t.Fatal(err)
			}
			signature, err := os.ReadFile(filepath.Join(dir, name+".sig"))
			if snapshot {
				if !os.IsNotExist(err) {
					t.Fatalf("snapshot signature: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !ed25519.Verify(public, archive, signature) {
					t.Fatal("signature did not verify")
				}
				s, err := store.New(store.Options{CacheDir: dir, Embedded: fstest.MapFS{}, Offline: true, Keys: []ed25519.PublicKey{public}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Load(context.Background(), opts.version); err != nil {
					t.Fatal(err)
				}
			}
			first := append([]byte(nil), archive...)
			if err := generate(opts); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Fatal("archive is not deterministic")
			}
			var compat struct {
				CLI       string `json:"cli"`
				Kit       string `json:"kit"`
				Minimum   string `json:"min_cli_for_templates"`
				Behaviors []int  `json:"behavior_versions"`
			}
			data, err := os.ReadFile(filepath.Join(dir, "compat.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &compat); err != nil {
				t.Fatal(err)
			}
			if compat.CLI != opts.version || compat.Kit != "v"+opts.version || compat.Minimum == "" || len(compat.Behaviors) == 0 {
				t.Fatalf("compat: %+v", compat)
			}
			var stack map[string]any
			data, err = os.ReadFile(filepath.Join(dir, "stack-versions.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &stack); err != nil {
				t.Fatal(err)
			}
			if stack["schema"] != float64(1) || stack["go_modules"] == nil {
				t.Fatalf("stack JSON lost YAML names: %v", stack)
			}
		})
	}
}

func TestRejectInvalidRelease(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, version, key string }{
		{"version", "../bad", ""}, {"missing key", "0.8.0", ""}, {"bad base64", "0.8.0", "!"}, {"short key", "0.8.0", "YQ=="},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := generate(options{root: "../../..", out: t.TempDir(), version: test.version, trustedKeys: []ed25519.PublicKey{make([]byte, ed25519.PublicKeySize)}, signingKey: test.key}); err == nil {
				t.Fatal("accepted invalid release")
			}
		})
	}
}

func TestSnapshotRemovesOldSignatures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	opts := options{root: "../../..", out: dir, version: "0.7.0", trustedKeys: []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, signingKey: base64.StdEncoding.EncodeToString(key)}
	if err := generate(opts); err != nil {
		t.Fatal(err)
	}
	opts.snapshot = true
	opts.version = "0.8.0-next"
	if err := generate(opts); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "compat.json" && entry.Name() != "stack-versions.json" && entry.Name() != "templates-v0.8.0-next.tar.zst" {
			t.Fatalf("stale release asset: %s", entry.Name())
		}
	}
}
