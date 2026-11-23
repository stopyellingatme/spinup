package console

import (
	"io/fs"
	"net/http"
)

func webHandler(_ config, _ fs.FS, _ *http.Client, _ clock) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "The console page is unavailable.\nTry: deploy a console image with the page implementation.", http.StatusServiceUnavailable)
	})
}
