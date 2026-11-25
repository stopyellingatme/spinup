package app

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPA(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"index.html":                   {Data: []byte("<!doctype html><title>SPA</title>")},
		"_app/immutable/app.abc123.js": {Data: []byte("console.log('app')")},
		"plain.txt":                    {Data: []byte("plain")},
		"folder/private.txt":           {Data: []byte("private")},
	}
	for _, tc := range []struct {
		method, path         string
		status               int
		cache, content, body string
	}{
		{"GET", "/", 200, "no-cache", "text/html", "SPA"},
		{"GET", "/index.html", 200, "no-cache", "text/html", "SPA"},
		{"GET", "/deep/link", 200, "no-cache", "text/html", "SPA"},
		{"HEAD", "/deep/link", 200, "no-cache", "text/html", ""},
		{"GET", "/_app/immutable/app.abc123.js", 200, "public, max-age=31536000, immutable", "javascript", "console.log"},
		{"HEAD", "/_app/immutable/app.abc123.js", 200, "public, max-age=31536000, immutable", "javascript", ""},
		{"GET", "/plain.txt", 200, "no-cache", "text/plain", "plain"},
		{"GET", "/folder/", 200, "no-cache", "text/html", "SPA"},
		{"GET", "/_app/immutable/missing.js", 404, "private, no-cache", "", ""},
		{"GET", "/api/missing", 404, "private, no-cache", "", ""},
		{"GET", "/api", 404, "private, no-cache", "", ""},
		{"GET", "/healthz/missing", 404, "private, no-cache", "", ""},
		{"GET", "/readyz/missing", 404, "private, no-cache", "", ""},
		{"GET", "/metrics", 404, "private, no-cache", "", ""},
		{"POST", "/deep/link", 405, "private, no-cache", "", ""},
		{"GET", "/healthz", 200, "private, no-cache", "application/json", `"status":"ok"`},
		{"GET", "/readyz", 200, "private, no-cache", "application/json", `"status":"ok"`},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			t.Parallel()
			a := New(WithLogger(discardLogger()), WithSPA(files))
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))
			if w.Code != tc.status || w.Header().Get("Cache-Control") != tc.cache || !strings.Contains(w.Header().Get("Content-Type"), tc.content) || !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("response = %d %v %s", w.Code, w.Header(), w.Body)
			}
			if tc.status == http.StatusMethodNotAllowed && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing allowed methods")
			}
			if tc.method == http.MethodHead && w.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
			if tc.status != 200 && strings.Contains(w.Body.String(), "SPA") {
				t.Fatal("reserved route returned the SPA")
			}
		})
	}
}

func TestSPAFileAccess(t *testing.T) {
	for _, seekable := range []bool{true, false} {
		t.Run(fmt.Sprintf("seekable=%v", seekable), func(t *testing.T) {
			files := &spaTestFS{MapFS: fstest.MapFS{
				"index.html":            {Data: []byte("<script>console.log('SPA')</script>")},
				"_app/immutable/app.js": {Data: []byte("0123456789")},
			}, seekable: seekable}
			handler := New(WithLogger(discardLogger()), WithSPA(files)).Handler()
			if files.reads["index.html"] != 1 || files.reads["_app/immutable/app.js"] != 0 {
				t.Fatalf("registration reads = %v", files.reads)
			}
			for range 2 {
				for _, path := range []string{"/deep/link", "/_app/immutable/app.js"} {
					req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
					if path != "/deep/link" {
						req.Header.Set("Range", "bytes=2-5")
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, req)
					if path == "/deep/link" {
						if w.Code != http.StatusOK || w.Body.String() != string(files.MapFS["index.html"].Data) {
							t.Fatalf("index = %d %q", w.Code, w.Body.String())
						}
					} else if w.Code != http.StatusPartialContent || w.Body.String() != "2345" {
						t.Fatalf("asset range = %d %q", w.Code, w.Body.String())
					}
				}
			}
			if files.reads["index.html"] != 1 || files.reads["_app/immutable/app.js"] != 0 {
				t.Errorf("requests used ReadFile: %v", files.reads)
			}
			if files.opens != 4 || files.closes != files.opens {
				t.Errorf("request file handles: opened %d, closed %d", files.opens, files.closes)
			}
		})
	}
}

type spaTestFS struct {
	fstest.MapFS
	seekable      bool
	reads         map[string]int
	opens, closes int
}

func (f *spaTestFS) ReadFile(name string) ([]byte, error) {
	if f.reads == nil {
		f.reads = make(map[string]int)
	}
	f.reads[name]++
	return f.MapFS.ReadFile(name)
}

func (f *spaTestFS) Open(name string) (fs.File, error) {
	file, err := f.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	f.opens++
	wrapped := spaTestFile{File: file, owner: f}
	if f.seekable {
		return spaTestSeekFile{spaTestFile: wrapped}, nil
	}
	return wrapped, nil
}

type spaTestFile struct {
	fs.File
	owner *spaTestFS
}

func (f spaTestFile) Close() error {
	f.owner.closes++
	return f.File.Close()
}

type spaTestSeekFile struct {
	spaTestFile
}

func (f spaTestSeekFile) Seek(offset int64, whence int) (int64, error) {
	return f.File.(io.ReadSeeker).Seek(offset, whence)
}

func TestSPACSP(t *testing.T) {
	t.Parallel()
	first := "\r\nconst greeting = 'Hello & goodbye';\r\n"
	second := "\n\tconsole.log('日本語');\n"
	for _, tc := range []struct {
		name, document string
		bodies         []string
	}{
		{"inline scripts", "<!doctype html><title>SPA</title><!-- <script>ignored</script> --><script type=module>" + first + "</script><SCRIPT SRC=\"/external.js\">external fallback</SCRIPT><script data-label=\"a > b\">" + second + "</script>", []string{first, second}},
		{"no inline scripts", "<title>SPA</title><script src=\"\">ignored</script>", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fstest.MapFS{
				"index.html":    {Data: []byte(tc.document)},
				"index.html.br": {Data: []byte("encoded index")},
				"other.html":    {Data: []byte("<title>Other</title>")},
			}
			handler := New(WithLogger(discardLogger()), WithSPA(files)).Handler()
			script := "script-src 'self'"
			for _, body := range tc.bodies {
				// The browser hashes the text after newline normalization.
				normalized := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
				sum := sha256.Sum256([]byte(normalized))
				script += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			}
			want := "default-src 'self'; " + script + "; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"
			for _, path := range []string{"/index.html", "/deep/link", "/other.html"} {
				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
				req.Header.Set("Accept-Encoding", "br")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if got := w.Header().Get("Content-Security-Policy"); w.Code != http.StatusOK || got != want {
					t.Errorf("%s: status = %d, CSP = %q, want %q", path, w.Code, got, want)
				}
				for _, directive := range strings.Split(w.Header().Get("Content-Security-Policy"), ";") {
					if strings.HasPrefix(strings.TrimSpace(directive), "script-src ") && strings.Contains(directive, "'unsafe-inline'") {
						t.Error("script-src permits unsafe-inline")
					}
				}
			}
		})
	}
}

func TestSPARepresentations(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"index.html":                {Data: []byte("<title>SPA</title>")},
		"index.html.br":             {Data: []byte("brotli index")},
		"index.html.gz":             {Data: []byte("gzip index")},
		"plain.txt":                 {Data: []byte("0123456789")},
		"plain.txt.br":              {Data: []byte("brotli plain")},
		"plain.txt.gz":              {Data: []byte("gzip plain")},
		"unknown":                   {Data: []byte("plain unknown")},
		"unknown.br":                {Data: []byte("brotli unknown")},
		"_app/immutable/unknown":    {Data: []byte("immutable unknown")},
		"_app/immutable/unknown.br": {Data: []byte("brotli immutable unknown")},
		"gzip.txt":                  {Data: []byte("gzip only")},
		"gzip.txt.gz":               {Data: []byte("gzip variant")},
		"orphan.gz":                 {Data: []byte("orphan")},
		"_app/immutable/app.js":     {Data: []byte("console.log('app')")},
		"_app/immutable/app.js.br":  {Data: []byte("brotli app")},
	}
	handler := New(WithLogger(discardLogger()), WithSPA(files)).Handler()
	for _, tc := range []struct {
		path, accept, encoding, body, original, suffix string
		vary                                           bool
	}{
		{"/plain.txt", "br, gzip", "br", "brotli plain", "plain.txt", ".br", true},
		{"/plain.txt", "br;q=0, gzip", "gzip", "gzip plain", "plain.txt", ".gz", true},
		{"/plain.txt", "br;q=0, gzip;q=0", "", "0123456789", "plain.txt", "", true},
		{"/plain.txt", "", "", "0123456789", "plain.txt", "", true},
		{"/plain.txt", "*", "br", "brotli plain", "plain.txt", ".br", true},
		{"/gzip.txt", "br, gzip", "gzip", "gzip variant", "gzip.txt", ".gz", true},
		{"/unknown", "br", "br", "brotli unknown", "unknown", ".br", true},
		{"/_app/immutable/unknown", "br", "br", "brotli immutable unknown", "_app/immutable/unknown", "", true},
		{"/orphan.gz", "br, gzip", "", "orphan", "orphan.gz", "", false},
		{"/deep/link", "br", "br", "brotli index", "index.html", ".br", true},
		{"/index.html", "gzip", "gzip", "gzip index", "index.html", ".gz", true},
		{"/plain.txt.br", "", "", "<title>SPA</title>", "index.html", "", true},
		{"/index.html.gz", "br", "br", "brotli index", "index.html", ".br", true},
		{"/_app/immutable/app.js", "br", "br", "brotli app", "_app/immutable/app.js", "", true},
		{"/_app/immutable/app.js", "", "", "console.log('app')", "_app/immutable/app.js", "", true},
	} {
		t.Run(tc.path+"/"+tc.accept, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			req.Header.Set("Accept-Encoding", tc.accept)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != tc.body || w.Header().Get("Content-Encoding") != tc.encoding {
				t.Fatalf("response = %d %v %q", w.Code, w.Header(), w.Body.String())
			}
			if got := w.Header().Get("Vary"); (got == "Accept-Encoding") != tc.vary {
				t.Errorf("Vary = %q, want Accept-Encoding: %v", got, tc.vary)
			}
			etag := ""
			cache := "public, max-age=31536000, immutable"
			if !strings.HasPrefix(tc.original, "_app/immutable/") {
				sum := sha256.Sum256(files[tc.original].Data)
				etag = fmt.Sprintf("\"%x%s\"", sum[:16], tc.suffix)
				cache = "no-cache"
			}
			if w.Header().Get("ETag") != etag || w.Header().Get("Cache-Control") != cache {
				t.Errorf("cache headers = %v, want ETag %q, Cache-Control %q", w.Header(), etag, cache)
			}
			if (strings.HasSuffix(tc.original, ".txt") || strings.HasSuffix(tc.original, "unknown")) && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
				t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
			}
			if tc.original == "index.html" && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
				t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
			}
			req.Method = http.MethodHead
			head := httptest.NewRecorder()
			handler.ServeHTTP(head, req)
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Encoding") != tc.encoding || head.Header().Get("ETag") != etag {
				t.Errorf("HEAD = %d %v %q", head.Code, head.Header(), head.Body.String())
			}
			req.Method = http.MethodGet
			if etag != "" {
				req.Header.Set("If-None-Match", etag)
				w = httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("ETag") != etag {
					t.Errorf("revalidation = %d %v %q", w.Code, w.Header(), w.Body.String())
				}
			}
		})
	}
	for _, name := range []string{"/_app/immutable/app.js.br", "/_app/immutable/missing.js.gz"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, name, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("sibling %s: status = %d", name, w.Code)
		}
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/plain.txt", nil)
	req.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("range = %d %v %q", w.Code, w.Header(), w.Body.String())
	}
}

func TestSPAReservedPaths(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{"index.html": {Data: []byte("SPA")}}
	a := New(WithLogger(discardLogger()), WithSPA(files))
	for _, prefix := range []string{"api", "healthz", "readyz", "metrics", "version", "_deploy", "_dev"} {
		for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			t.Run(method+"/"+prefix+"/missing", func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, "/"+prefix+"/missing", nil))
				if w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" || strings.Contains(w.Body.String(), "SPA") {
					t.Fatalf("response = %d %v %s", w.Code, w.Header(), w.Body)
				}
			})
		}
	}
}

// A client can send Accept-Encoding in more than one field. A refusal in the
// second field must count.
func TestSPASplitAcceptEncoding(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"index.html":   {Data: []byte("<title>SPA</title>")},
		"plain.txt":    {Data: []byte("0123456789")},
		"plain.txt.br": {Data: []byte("brotli plain")},
		"plain.txt.gz": {Data: []byte("gzip plain")},
	}
	handler := New(WithLogger(discardLogger()), WithSPA(files)).Handler()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/plain.txt", nil)
	req.Header.Add("Accept-Encoding", "*")
	req.Header.Add("Accept-Encoding", "br;q=0, gzip;q=0")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" || w.Body.String() != "0123456789" {
		t.Fatalf("response = %d %v %q, want the plain file", w.Code, w.Header(), w.Body.String())
	}
}

// TestSPAOptions covers a Vite layout: hashed files in assets/, no _app directory.
func TestSPAOptions(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"index.html":                  {Data: []byte(`<!doctype html><script type="module" src="/assets/app-abc.js"></script>`)},
		"assets/app-abc.js":           {Data: []byte("console.log('app')")},
		"assets/app-abc.js.br":        {Data: []byte("brotli app")},
		"_app/old.js":                 {Data: []byte("not an asset dir here")},
		"favicon.svg":                 {Data: []byte("<svg/>")},
		"assets/app-abc.js.gz":        {Data: []byte("gzip app")},
		"assets/not-a-shell-fallback": {Data: []byte("x")},
	}
	a := New(WithLogger(discardLogger()), WithSPAOptions(files, SPAOptions{AssetDir: "assets/", ImmutablePrefix: "assets/"}))
	get := func(path, encoding string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), "GET", path, nil)
		if encoding != "" {
			r.Header.Set("Accept-Encoding", encoding)
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	w := get("/assets/app-abc.js", "br")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || w.Header().Get("ETag") != "" || w.Header().Get("Content-Encoding") != "br" || w.Body.String() != "brotli app" {
		t.Fatalf("hashed asset = %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := get("/assets/missing.js", ""); w.Code != 404 || strings.Contains(w.Body.String(), "<script") {
		t.Fatalf("missing asset = %d %s", w.Code, w.Body)
	}
	w = get("/deep/link", "")
	if w.Code != 200 || w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("shell = %d %v", w.Code, w.Header())
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'self'; script-src 'self';") {
		t.Fatalf("csp = %q", csp)
	}
	// The default layout is not an asset directory here: _app/ paths fall back to the shell.
	if w := get("/_app/missing.js", ""); w.Code != 200 {
		t.Fatalf("_app path = %d", w.Code)
	}
}

// TestSPAOptionsDirs checks that a directory option without slashes matches only that directory.
func TestSPAOptionsDirs(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, fallback, want string }{
		{"", "_app/", "_app/"},
		{"/", "_app/", "_app/"},
		{".", "_app/", "_app/"},
		{"..", "_app/", "_app/"},
		{"assets", "_app/", "assets/"},
		{"/assets/", "_app/", "assets/"},
		{"assets//", "_app/", "assets/"},
		{"static/hashed", "_app/", "static/hashed/"},
		{"../assets", "_app/", "assets/"},
	}
	for _, tt := range tests {
		if got := spaDir(tt.in, tt.fallback); got != tt.want {
			t.Errorf("spaDir(%q, %q) = %q, want %q", tt.in, tt.fallback, got, tt.want)
		}
	}
	files := fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html>")},
		"assets/app-abc.js":  {Data: []byte("a")},
		"assets-old/keep.js": {Data: []byte("b")},
	}
	a := New(WithLogger(discardLogger()), WithSPAOptions(files, SPAOptions{AssetDir: "/assets", ImmutablePrefix: "assets"}))
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
		return w
	}
	if w := get("/assets/app-abc.js"); w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("hashed asset = %d %v", w.Code, w.Header())
	}
	if w := get("/assets-old/keep.js"); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("ETag") == "" {
		t.Fatalf("sibling directory = %d %v, want a file that revalidates", w.Code, w.Header())
	}
	if w := get("/assets-old/missing.js"); w.Code != 200 {
		t.Fatalf("missing file in a sibling directory = %d, want the shell", w.Code)
	}
}

// TestSPAOptionsSplit checks that AssetDir and ImmutablePrefix are independent: the 404 rule
// follows AssetDir, and the cache rule follows ImmutablePrefix.
func TestSPAOptionsSplit(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html>")},
		"assets/hashed/a-abc.js": {Data: []byte("a")},
		"assets/b.js":            {Data: []byte("b")},
	}
	a := New(WithLogger(discardLogger()), WithSPAOptions(files, SPAOptions{AssetDir: "assets/", ImmutablePrefix: "assets/hashed/"}))
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
		return w
	}
	if w := get("/assets/hashed/a-abc.js"); w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || w.Header().Get("ETag") != "" {
		t.Fatalf("hashed file = %d %v, want immutable and no ETag", w.Code, w.Header())
	}
	if w := get("/assets/b.js"); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("ETag") == "" {
		t.Fatalf("plain asset = %d %v, want no-cache and an ETag", w.Code, w.Header())
	}
	for _, path := range []string{"/assets/missing.js", "/assets/hashed/missing.js"} {
		if w := get(path); w.Code != 404 {
			t.Fatalf("%s = %d, want 404", path, w.Code)
		}
	}
}
