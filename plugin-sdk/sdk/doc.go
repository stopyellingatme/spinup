// Package sdk defines API 1 for executable spinup plugins.
// Plugins implement Handler and call Serve to exchange JSON-RPC 2.0 objects,
// one per line, over stdin and stdout. Diagnostics belong on stderr.
package sdk
