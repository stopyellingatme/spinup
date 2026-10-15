package deploy

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func tarOf(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	if err := w.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := w.WriteHeader(&tar.Header{Name: "./" + name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(files[name]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func appendFile(t *testing.T, root, name, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, name), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}
