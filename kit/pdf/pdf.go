package pdf

import (
	"errors"
	"net/http"
)

// MaxBytes is the largest PDF that Serve accepts: 20 MiB.
const MaxBytes = 20 << 20

// Serve checks the sidecar response and writes the PDF as an attachment with
// httpx.WriteAttachment. It always closes resp.Body. It accepts only status 200
// with the type application/pdf, reads at most MaxBytes plus one byte, and checks
// that the body starts with %PDF-. A sidecar 504 or a context deadline answers
// 504, a sidecar 503 or a connection error answers 503, and any other failure
// answers 502. The answer holds no sidecar text. The returned error does.
//
// This is the contract stub of the pre-seed. Unit K replaces the body.
func Serve(w http.ResponseWriter, resp *http.Response, filename string) error {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	_ = filename
	w.WriteHeader(http.StatusBadGateway)
	return errors.New("pdf: Serve is not implemented")
}
