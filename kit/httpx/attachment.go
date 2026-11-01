package httpx

import "net/http"

// WriteAttachment writes body as a download (ADR 0026 decision 6.1). It sets
// Content-Type, Content-Length, Cache-Control: private, no-store,
// X-Content-Type-Options: nosniff, and Content-Disposition: attachment with an
// ASCII filename and an RFC 5987 filename*. It removes quotes, slashes, control
// characters, and line breaks from filename. An empty result becomes "download".
//
// This is the contract stub of the pre-seed. Unit K replaces the body.
func WriteAttachment(w http.ResponseWriter, filename, contentType string, body []byte) {
	_, _, _ = filename, contentType, body
	http.Error(w, "httpx: WriteAttachment is not implemented", http.StatusNotImplemented)
}
