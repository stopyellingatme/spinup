package tools

import (
	"archive/zip"
	"errors"
	"io"
)

func installTofu(source, destination, goos string) error {
	archive, err := zip.OpenReader(source)
	if err != nil {
		return archiveError(err)
	}
	defer archive.Close()
	var binary []byte
	seen := map[string]bool{}
	for _, file := range archive.File {
		if seen[file.Name] || !file.Mode().IsRegular() {
			return errors.New("OpenTofu release archive contains duplicate or unsafe entries\nTry: check the pinned release asset")
		}
		seen[file.Name] = true
		switch file.Name {
		case "LICENSE", "README.md", "CHANGELOG.md":
			continue
		default:
			if file.Name != executableName("tofu", goos) {
				return errors.New("OpenTofu release archive contains an unsafe path\nTry: check the pinned release asset")
			}
		}
		if file.UncompressedSize64 > maxDownloadSize {
			return archiveError(nil)
		}
		reader, err := file.Open()
		if err != nil {
			return archiveError(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxDownloadSize+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > maxDownloadSize {
			return archiveError(errors.Join(readErr, closeErr))
		}
		binary = data
	}
	if len(binary) == 0 {
		return errors.New("OpenTofu release archive misses the tofu binary\nTry: check the pinned release asset")
	}
	return installBytes(binary, destination)
}
