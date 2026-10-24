package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name string
	typ  byte
	body string
}

func tarEntries(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var result bytes.Buffer
	gz := gzip.NewWriter(&result)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		header := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0755}
		switch e.typ {
		case tar.TypeReg:
			header.Size = int64(len(e.body))
		case tar.TypeSymlink, tar.TypeLink:
			header.Linkname = "age/age"
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "age.tar.gz")
	if err := os.WriteFile(path, result.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAgeTarFiles uses the entry list of the real age v1.3.2 archive.
func TestAgeTarFiles(t *testing.T) {
	reg := func(name string) tarEntry { return tarEntry{name, tar.TypeReg, "bin-" + name} }
	real := []tarEntry{
		{"age/", tar.TypeDir, ""}, reg("age/age"), reg("age/age-keygen"), reg("age/age-inspect"),
		reg("age/age-plugin-tag"), reg("age/age-plugin-batchpass"), reg("age/age-plugin-pq"),
		reg("age/age-plugin-tagpq"), reg("age/LICENSE"),
	}
	files, err := ageTarFiles(tarEntries(t, real))
	if err != nil || len(files) != 2 || string(files["age"]) != "bin-age/age" || len(files["age-keygen"]) == 0 {
		t.Fatalf("real layout = %v, %v", files, err)
	}
	base := []tarEntry{reg("age/age"), reg("age/age-keygen")}
	for name, extra := range map[string][]tarEntry{
		"absolute":  {reg("/etc/passwd")},
		"traversal": {reg("age/../evil")},
		"nested":    {reg("age/sub/tool")},
		"other dir": {reg("other/tool")},
		"symlink":   {{"age/link", tar.TypeSymlink, ""}},
		"hardlink":  {{"age/link", tar.TypeLink, ""}},
		"device":    {{"age/dev", tar.TypeChar, ""}},
		"duplicate": {reg("age/age")},
		"dup other": {reg("age/LICENSE"), reg("age/LICENSE")},
	} {
		entries := append(append([]tarEntry{}, base...), extra...)
		if _, err := ageTarFiles(tarEntries(t, entries)); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if _, err := ageTarFiles(tarEntries(t, []tarEntry{reg("age/age"), reg("age/LICENSE")})); err == nil {
		t.Error("missing age-keygen accepted")
	}
}

func ageZip(t *testing.T, names ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "age.zip")
	if err := os.WriteFile(path, tofuZip(t, names...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgeZipFiles(t *testing.T) {
	files, err := ageZipFiles(ageZip(t, "age/age.exe", "age/age-keygen.exe", "age/age-inspect.exe", "age/age-plugin-pq.exe", "age/LICENSE"))
	if err != nil || len(files) != 2 {
		t.Fatalf("real layout = %v, %v", files, err)
	}
	base := []string{"age/age.exe", "age/age-keygen.exe"}
	for name, extra := range map[string]string{
		"absolute":  "/evil.exe",
		"traversal": "age/../evil.exe",
		"nested":    "age/sub/x.exe",
		"backslash": `age/..\evil.exe`,
		"symlink":   "symlink:age/link",
		"duplicate": "age/age.exe",
	} {
		entries := append(append([]string{}, base...), extra)
		if _, err := ageZipFiles(ageZip(t, entries...)); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if _, err := ageZipFiles(ageZip(t, "age/age.exe", "age/LICENSE")); err == nil {
		t.Error("missing age-keygen accepted")
	}
}
