// Package codemod edits Go, YAML, TOML, and JSON documents through their syntax.
// The editors preserve comments and return original bytes for unchanged edits.
// Go edits use go/format. YAML edits use two spaces for indentation.
// TOML edits preserve byte ranges outside the changed entry. JSON edits retain
// object key order and the source indentation.
package codemod
