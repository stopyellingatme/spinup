// Command templates creates the release assets without network access.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"go.yaml.in/yaml/v3"
)

type options struct {
	trustedKeys                    []ed25519.PublicKey
	root, out, version, signingKey string
	snapshot                       bool
}

func main() {
	var o options
	flag.StringVar(&o.root, "root", ".", "repository root")
	flag.StringVar(&o.out, "out", "dist/extra", "output directory")
	flag.StringVar(&o.version, "version", "", "release version without v")
	flag.BoolVar(&o.snapshot, "snapshot", false, "omit the signature")
	flag.Parse()
	o.signingKey = os.Getenv("SPINUP_TEMPLATES_SIGNING_KEY")
	o.trustedKeys = store.TrustedKeys
	if err := generate(o); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create release assets: %v\nTry: check the version, repository, and SPINUP_TEMPLATES_SIGNING_KEY\n", err)
		os.Exit(1)
	}
}

func generate(o options) error {
	version, err := compat.ParseVersion(o.version)
	if err != nil || strings.HasPrefix(o.version, "v") {
		return fmt.Errorf("invalid release version %q", o.version)
	}
	var key ed25519.PrivateKey
	if !o.snapshot {
		if len(o.trustedKeys) == 0 {
			return errors.New("store.TrustedKeys is empty")
		}
		decoded, err := base64.StdEncoding.DecodeString(o.signingKey)
		if err != nil || len(decoded) != ed25519.PrivateKeySize {
			return errors.New("signing key must be a base64 ed25519 private key (64 bytes)")
		}
		key = ed25519.PrivateKey(decoded)
		if !bytes.Equal(key, ed25519.NewKeyFromSeed(key.Seed())) {
			return errors.New("signing key has an invalid public half")
		}
		matched := false
		for _, public := range o.trustedKeys {
			if public.Equal(key.Public()) {
				matched = true
			}
		}
		if !matched {
			return errors.New("signing key does not match store.TrustedKeys")
		}
	}
	stackYAML, err := os.ReadFile(filepath.Join(o.root, "internal/stackversions/stackversions.yaml"))
	if err != nil {
		return err
	}
	if _, err := stackversions.Parse(stackYAML); err != nil {
		return err
	}
	// Decode the YAML separately to preserve its field names in JSON.
	var stack map[string]any
	if err := yaml.Unmarshal(stackYAML, &stack); err != nil {
		return err
	}
	stackJSON, err := json.MarshalIndent(stack, "", "  ")
	if err != nil {
		return err
	}
	notes, err := os.ReadFile(filepath.Join(o.root, "kit/MIGRATION.json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var archive bytes.Buffer
	if err := store.WriteArchive(&archive, os.DirFS(filepath.Join(o.root, "internal/templates/packs")), stackYAML, notes); err != nil {
		return err
	}
	if _, err := store.ReadArchive(archive.Bytes(), o.version); err != nil {
		return err
	}
	metadata := struct {
		CLI       string `json:"cli"`
		Kit       string `json:"kit"`
		Minimum   string `json:"min_cli_for_templates"`
		Behaviors []int  `json:"behavior_versions"`
	}{o.version, version.String(), o.version, []int{1}}
	compatJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	// This directory holds only the current release assets, even across versions.
	old, err := filepath.Glob(filepath.Join(o.out, "templates-v*.tar.zst*"))
	if err != nil {
		return err
	}
	for _, name := range old {
		if err := os.Remove(name); err != nil {
			return err
		}
	}
	name := filepath.Join(o.out, "templates-v"+o.version+".tar.zst")
	for _, file := range []struct {
		name string
		data []byte
	}{
		{name, archive.Bytes()},
		{filepath.Join(o.out, "compat.json"), append(compatJSON, '\n')},
		{filepath.Join(o.out, "stack-versions.json"), append(stackJSON, '\n')},
	} {
		if err := os.WriteFile(file.name, file.data, 0o644); err != nil {
			return err
		}
	}
	if o.snapshot {
		// Remove a signature left by a previous signed invocation in this directory.
		if err := os.Remove(name + ".sig"); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(name+".sig", ed25519.Sign(key, archive.Bytes()), 0o644)
}
