// Command convert-templates converts the Go templates to minijinja templates (R0.2,
// docs/design/rust-plan.md decision 3). The core is main-loop only. The golden unit of a
// pack owns rules/<pack>.go.
//
//	go run ./scripts/rust/convert-templates [-pack base,api] [-out crates/spinup-packs/packs]
//	go run ./scripts/rust/convert-templates -spike
//
// The default mode writes the Rust packs (make rs-convert-packs). The -spike mode writes the
// R0.2 spike tree: all packs, the inline generator templates, and the inventory.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "crates/spinup-packs/packs", "output tree, relative to -root or absolute")
	pack := flag.String("pack", "", "comma-separated pack names; empty converts every pack")
	spike := flag.Bool("spike", false, "write the R0.2 spike tree (default -out spikes-rs/r0-02-minijinja/packs)")
	flag.Parse()
	outSet := false
	flag.Visit(func(f *flag.Flag) { outSet = outSet || f.Name == "out" })
	if *spike && !outSet {
		*out = "spikes-rs/r0-02-minijinja/packs"
	}
	dest := *out
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(*root, dest)
	}
	var err error
	if *spike {
		err = convertRepository(*root, dest)
	} else {
		var names []string
		if *pack != "" {
			names = strings.Split(*pack, ",")
		}
		err = convertPacks(*root, dest, names)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
