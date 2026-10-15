package store

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
)

func stackData(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../stackversions/stackversions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestArchiveRoundTrip(t *testing.T) {
	t.Parallel()
	packs := fstest.MapFS{
		"base/.gitignore": &fstest.MapFile{Data: []byte("secret\n"), Mode: 0644},
		"base/run.sh":     &fstest.MapFile{Data: []byte("#!/bin/sh\n"), Mode: 0755 | fs.ModeSetuid},
	}
	var first, second bytes.Buffer
	for _, w := range []*bytes.Buffer{&first, &second} {
		if err := WriteArchive(w, packs, stackData(t), []byte(`{"schema":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("archive bytes are not deterministic")
	}
	zr, err := zstd.NewReader(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	previous := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name <= previous || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.ModTime.Unix() != 0 || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
			t.Fatalf("unstable archive header: %#v", h)
		}
		previous = h.Name
	}
	version, err := ReadArchive(first.Bytes(), "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(version.Packs, "base/.gitignore", "base/run.sh"); err != nil {
		t.Fatal(err)
	}
	if version.Version != "1.2.3" || version.Stack == nil || string(version.KitMigration) != `{"schema":1}` {
		t.Fatalf("version = %#v", version)
	}
	for name, want := range packs {
		got, err := fs.ReadFile(version.Packs, name)
		if err != nil || !bytes.Equal(got, want.Data) {
			t.Fatalf("read %s: %q, %v", name, got, err)
		}
		info, err := fs.Stat(version.Packs, name)
		if err != nil || info.Mode() != want.Mode {
			t.Fatalf("mode %s: %v, %v", name, info, err)
		}
	}
}

func rawArchive(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestArchiveRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		headers []*tar.Header
		want    string
	}{
		{"absolute", []*tar.Header{{Name: "/packs/x"}}, "unsafe archive path"},
		{"parent", []*tar.Header{{Name: "packs/../x"}}, "unsafe archive path"},
		{"nested parent", []*tar.Header{{Name: "packs/a/../b"}}, "unsafe archive path"},
		{"symlink", []*tar.Header{{Name: "packs/x", Typeflag: tar.TypeSymlink, Linkname: "target"}}, "unsupported archive entry"},
		{"hardlink", []*tar.Header{{Name: "packs/x", Typeflag: tar.TypeLink, Linkname: "target"}}, "unsupported archive entry"},
		{"device", []*tar.Header{{Name: "packs/x", Typeflag: tar.TypeChar}}, "unsupported archive entry"},
		{"unexpected", []*tar.Header{{Name: "other"}}, "unexpected archive entry"},
		{"stack directory", []*tar.Header{{Name: "stackversions.yaml/", Typeflag: tar.TypeDir}}, "invalid entry type"},
		{"duplicate", []*tar.Header{{Name: "packs/x"}, {Name: "packs/x"}}, "duplicate archive entry"},
		{"missing stack", []*tar.Header{{Name: "packs/x"}}, "archive requires"},
		{"missing packs", []*tar.Header{{Name: "MIGRATION.json"}}, "archive requires"},
		{"backslash", []*tar.Header{{Name: `packs/..\escape`}}, "unsafe archive path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ReadArchive(rawArchive(t, tc.headers), "1.0.0"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
	t.Run("entry count", func(t *testing.T) {
		headers := make([]*tar.Header, 10001)
		for i := range headers {
			headers[i] = &tar.Header{Name: "packs/" + string(rune(0x1000+i))}
		}
		if _, err := ReadArchive(rawArchive(t, headers), "1.0.0"); err == nil || !strings.Contains(err.Error(), "10000") {
			t.Fatal("accepted too many entries")
		}
	})
	t.Run("size", func(t *testing.T) {
		var buf bytes.Buffer
		zw, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(zw)
		if err := tw.WriteHeader(&tar.Header{Name: "packs/large", Size: 50<<20 + 1}); err != nil {
			t.Fatal(err)
		}
		// The reader must reject the declared size before it reads the body.
		_ = tw.Close()
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadArchive(buf.Bytes(), "1.0.0"); err == nil || !strings.Contains(err.Error(), "50 MiB") {
			t.Fatal("accepted oversized archive")
		}
	})
}
