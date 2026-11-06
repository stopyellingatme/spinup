//go:build !darwin

package vfs

import "os"

func syncFile(file *os.File) error {
	return file.Sync()
}

func syncBatch(string) error {
	return nil
}
