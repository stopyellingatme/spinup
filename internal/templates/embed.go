package templates

import (
	"embed"
	"io/fs"
)

//go:embed all:packs
var embedded embed.FS

// Embedded returns the embedded file system rooted at packs.
func Embedded() fs.FS {
	packs, err := fs.Sub(embedded, "packs")
	if err != nil {
		panic(err)
	}
	return packs
}
