package app

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// WithSPA serves a static application from files, whose root contains index.html.
// GET and HEAD requests for client routes receive index.html without directory listings.
// API, health, metrics, version, and internal routes never receive the fallback.
// Files must remain unchanged after route registration.
func WithSPA(files fs.FS) Option {
	return WithRoutes(func(mux *http.ServeMux) {
		index, csp := indexSPA(files)
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			for _, prefix := range []string{"api", "healthz", "readyz", "metrics", "version", "_deploy", "_dev"} {
				if name == prefix || strings.HasPrefix(name, prefix+"/") {
					http.NotFound(w, r)
					return
				}
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			file, found := index[name]
			if !found {
				if strings.HasPrefix(name, "_app/") {
					http.NotFound(w, r)
					return
				}
				name = "index.html"
				file, found = index[name]
				if !found {
					http.NotFound(w, r)
					return
				}
			}
			encoding, suffix := "", ""
			if file.br || file.gzip {
				if file.br && httpx.AcceptsEncoding(r.Header.Get("Accept-Encoding"), "br") {
					encoding, suffix = "br", ".br"
				} else if file.gzip && httpx.AcceptsEncoding(r.Header.Get("Accept-Encoding"), "gzip") {
					encoding, suffix = "gzip", ".gz"
				}
			}
			f, err := files.Open(name + suffix)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			content, ok := f.(io.ReadSeeker)
			if !ok {
				data, err := io.ReadAll(f)
				if err != nil {
					http.NotFound(w, r)
					return
				}
				content = bytes.NewReader(data)
			}
			if file.br || file.gzip {
				w.Header().Add("Vary", "Accept-Encoding")
			}
			if encoding != "" {
				w.Header().Set("Content-Encoding", encoding)
			}
			w.Header().Set("Content-Type", file.contentType)
			if file.etag != "" {
				w.Header().Set("ETag", "\""+file.etag+suffix+"\"")
			}
			w.Header().Set("Cache-Control", "no-cache")
			if strings.HasPrefix(file.contentType, "text/html") {
				w.Header().Set("Content-Security-Policy", csp)
			}
			if strings.HasPrefix(name, "_app/immutable/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeContent(w, r, name, time.Time{}, content)
		}))
	})
}
