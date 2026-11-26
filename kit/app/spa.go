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

// SPAOptions names the asset directories of a static build. The zero value is the
// SvelteKit layout: AssetDir "_app/" and ImmutablePrefix "_app/immutable/".
// The two fields are independent. A build with another layout sets both.
type SPAOptions struct {
	// AssetDir is the directory of the build assets, such as "assets/". A missing file
	// under it is a 404, not the shell.
	AssetDir string
	// ImmutablePrefix is the directory of the files whose names carry a content hash, such as
	// "assets/". They get a long Cache-Control and no ETag. Name only a directory where each
	// file name changes when the content changes.
	ImmutablePrefix string
}

func (o SPAOptions) withDefaults() SPAOptions {
	o.AssetDir = spaDir(o.AssetDir, "_app/")
	o.ImmutablePrefix = spaDir(o.ImmutablePrefix, "_app/immutable/")
	return o
}

// spaDir returns dir as a slash-separated path with no leading slash and one trailing slash,
// so a prefix test cannot match a sibling such as "assets-old/". An empty dir, or one that
// names the root, gives the fallback.
func spaDir(dir, fallback string) string {
	dir = strings.Trim(path.Clean("/"+dir), "/")
	if dir == "" {
		return fallback
	}
	return dir + "/"
}

// WithSPA serves a static application from files, whose root contains index.html.
// GET and HEAD requests for client routes receive index.html without directory listings.
// API, health, metrics, version, and internal routes never receive the fallback.
// Files must remain unchanged after route registration.
func WithSPA(files fs.FS) Option { return WithSPAOptions(files, SPAOptions{}) }

// WithSPAOptions is WithSPA for a build with another asset layout, for example a Vite build
// with hashed files in "assets/".
func WithSPAOptions(files fs.FS, opts SPAOptions) Option {
	opts = opts.withDefaults()
	return WithRoutes(func(mux *http.ServeMux) {
		index, csp := indexSPA(files, opts.ImmutablePrefix)
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
				if strings.HasPrefix(name, opts.AssetDir) {
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
				// A client can send the header in more than one field.
				accept := strings.Join(r.Header.Values("Accept-Encoding"), ",")
				if file.br && httpx.AcceptsEncoding(accept, "br") {
					encoding, suffix = "br", ".br"
				} else if file.gzip && httpx.AcceptsEncoding(accept, "gzip") {
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
			if strings.HasPrefix(name, opts.ImmutablePrefix) {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeContent(w, r, name, time.Time{}, content)
		}))
	})
}
