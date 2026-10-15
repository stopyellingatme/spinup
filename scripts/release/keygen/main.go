// Command keygen prints a base64 ed25519 key pair without writing files.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot generate template key: %v\nTry: check the operating system random source\n", err)
		os.Exit(1)
	}
	// Only the secret goes to stdout. The public value and instructions go to stderr.
	fmt.Fprintln(os.Stdout, base64.StdEncoding.EncodeToString(private))
	fmt.Fprintln(os.Stderr, "TrustedKeys public key (base64): "+base64.StdEncoding.EncodeToString(public))
}
