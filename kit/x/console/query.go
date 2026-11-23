package console

import "net/http"

func queryHandler(_ *http.Client, _ clock) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Console queries are unavailable.\nTry: deploy a console image with the query implementation.", http.StatusServiceUnavailable)
	})
}
