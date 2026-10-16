package apidocs

import (
	"bytes"
	"compress/gzip"
	"embed"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// Options configures the API reference and its OpenAPI document.
type Options struct {
	// Spec contains bundle.gen.yaml at its root and supplies the fallback document.
	Spec fs.FS
	// Dir contains the development document, read on each request before Spec.
	Dir string
	// Title sets the HTML title. The default is "API reference".
	Title string
}

//go:embed assets/scalar.js.gz
var scalarGzip []byte

//go:embed static/index.html static/boot.js
var static embed.FS

var page = template.Must(template.ParseFS(static, "static/index.html"))

const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Mount registers the OpenAPI document and the self-hosted Scalar reference routes.
func Mount(mux *http.ServeMux, opts Options) {
	if opts.Title == "" {
		opts.Title = "API reference"
	}
	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		headers(w, "application/yaml", "no-cache")
		data, err := readSpec(opts)
		if err != nil {
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusNotFound, Detail: "The OpenAPI document is missing.\nTry: run spinup g resource or spinup g openapi"})
			return
		}
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /api/docs", func(w http.ResponseWriter, _ *http.Request) {
		headers(w, "text/html; charset=utf-8", "no-cache")
		_ = page.Execute(w, opts.Title)
	})
	mux.HandleFunc("GET /api/docs/boot.js", func(w http.ResponseWriter, _ *http.Request) {
		headers(w, "text/javascript; charset=utf-8", "no-cache")
		data, _ := static.ReadFile("static/boot.js")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /api/docs/scalar.js", func(w http.ResponseWriter, r *http.Request) {
		headers(w, "text/javascript; charset=utf-8", "public, max-age=86400")
		w.Header().Add("Vary", "Accept-Encoding")
		if acceptsGzip(strings.Join(r.Header.Values("Accept-Encoding"), ",")) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(scalarGzip)
			return
		}
		reader, err := gzip.NewReader(bytes.NewReader(scalarGzip))
		if err != nil {
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusInternalServerError, Detail: "Cannot read the API reference asset.\nTry: rebuild the application with a valid Scalar bundle"})
			return
		}
		defer reader.Close()
		_, _ = io.Copy(w, reader)
	})
}

func headers(w http.ResponseWriter, contentType, cache string) {
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cache)
}

func readSpec(opts Options) ([]byte, error) {
	if opts.Dir != "" {
		data, err := os.ReadFile(filepath.Join(opts.Dir, "bundle.gen.yaml"))
		if err == nil {
			return data, nil
		}
	}
	if opts.Spec != nil {
		return fs.ReadFile(opts.Spec, "bundle.gen.yaml")
	}
	return nil, fs.ErrNotExist
}

func acceptsGzip(value string) bool {
	wildcard := false
	for token := range strings.SplitSeq(value, ",") {
		parts := strings.Split(token, ";")
		name := strings.TrimSpace(parts[0])
		if !strings.EqualFold(name, "gzip") && name != "*" {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, raw, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(raw, 64)
				quality = 0
				if err == nil && parsed > 0 && parsed <= 1 {
					quality = parsed
				}
			}
		}
		if strings.EqualFold(name, "gzip") {
			return quality > 0
		}
		wildcard = quality > 0
	}
	return wildcard
}
