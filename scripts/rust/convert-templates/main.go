// Command convert-templates converts the Go oracle templates for the R0.2 spike.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "spikes-rs/r0-02-minijinja/packs", "output tree, relative to root")
	flag.Parse()
	if err := convertRepository(*root, filepath.Join(*root, *out)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
