package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/scripts/rust/convert-templates/rules"
)

// engineMarker is the content of <out>/ENGINE: the template engine of the converted packs
// (ADR 0020 §3). The Rust CLI rejects a pack tree without it.
const engineMarker = "minijinja\n"

// convertPacks converts the Go packs in internal/templates/packs into out/<pack>/ for the
// Rust CLI (make rs-convert-packs). An empty names list converts every pack. The converter
// replaces each named pack directory, so files that the Go pack removed go away too. Then
// it applies the rules of each pack (rules/<pack>.go) and writes out/ENGINE.
func convertPacks(root, out string, names []string) error {
	src := filepath.Join(root, "internal/templates/packs")
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read the Go packs: %w\nTry: run the converter from the repository root", err)
	}
	var all []string
	for _, entry := range entries {
		if entry.IsDir() {
			all = append(all, entry.Name())
		}
	}
	if len(names) == 0 {
		names = all
	}
	for _, name := range names {
		if !slices.Contains(all, name) {
			return fmt.Errorf("convert pack %q: no such pack in %s\nTry: use one of %s", name, src, strings.Join(all, ", "))
		}
	}
	for _, name := range names {
		if err := convertPack(src, out, name); err != nil {
			return err
		}
	}
	if err := writeFile(filepath.Join(out, "ENGINE"), []byte(engineMarker)); err != nil {
		return err
	}
	fmt.Printf("converted %d packs into %s\n", len(names), out)
	return nil
}

func convertPack(src, out, name string) error {
	dest := filepath.Join(out, name)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	rule, _ := rules.For(name)
	packDir := filepath.Join(src, name)
	return filepath.WalkDir(packDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(packDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data, _, err = convertFile(name+"/"+rel, data)
		if err != nil {
			return fmt.Errorf("convert %s/%s: %w\nTry: fix the template, or add a rule in scripts/rust/convert-templates/rules/", name, rel, err)
		}
		if rule.Fix != nil {
			data, err = rule.Fix(rel, data)
			if err != nil {
				return fmt.Errorf("apply the %s rule to %s: %w\nTry: fix scripts/rust/convert-templates/rules/", name, rel, err)
			}
		}
		return writeFile(filepath.Join(dest, filepath.FromSlash(rel)), data)
	})
}
