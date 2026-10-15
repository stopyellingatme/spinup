// Package remote runs commands and uploads files through the system SSH client.
// It preserves SSH configuration and shares connections through a private cache.
// Remote arguments use POSIX shell quoting. Uploads never put file data in argv.
package remote
