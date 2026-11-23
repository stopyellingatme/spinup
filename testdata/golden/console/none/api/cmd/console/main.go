package main

import (
	"os"

	"github.com/stopyellingatme/spinup/kit/x/console"
)

func main() {
	os.Exit(console.Main(os.Args[1:], os.Stdout, os.Stderr))
}
