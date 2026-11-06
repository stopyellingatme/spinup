package resource

import (
	"fmt"
	"strings"
)

// jsonBodyTest checks the registered rules without a database or a socket.
func (rc resourceCode) jsonBodyTest(g *goFile, base string) {
	var parts []string
	for _, f := range rc.fields {
		value := sample(f, false, "")
		if f.Type.Ref != "" && !f.Nullable {
			value = "1"
		}
		parts = append(parts, fmt.Sprintf("%q:%s", f.col, value))
	}
	body := "{" + strings.Join(parts, ",") + "}"
	first := rc.fields[0].col
	g.p(`
// Test%sChecksJSONBody checks the body before the handler decodes it.
func Test%sChecksJSONBody(t *testing.T) {
	t.Parallel()
	valid := %q
	oversized := valid + strings.Repeat(" ", 1<<20)
	tests := []struct {
		name, body string
		status int
		unknownLength bool
	}{
		{"valid", valid, http.StatusNoContent, false},
		{"valid without Content-Length", valid, http.StatusNoContent, true},
		{"at limit", valid + strings.Repeat(" ", (1<<20)-len(valid)), http.StatusNoContent, false},
		{"case variant", %q, http.StatusBadRequest, false},
		{"duplicate name", %q, http.StatusBadRequest, false},
		{"unknown member", %q, http.StatusBadRequest, false},
		{"invalid UTF-8", %q, http.StatusBadRequest, false},
		{"oversized", oversized, http.StatusRequestEntityTooLarge, false},
		{"oversized without Content-Length", oversized, http.StatusRequestEntityTooLarge, true},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, tc := range tests {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				called := false
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					data, err := io.ReadAll(r.Body)
					if err != nil { t.Fatal(err) }
					if string(data) != tc.body { t.Fatal("the middleware changed the body") }
					var input gen.%sInput
					if err := json.Unmarshal(data, &input); err != nil { t.Fatal(err) }
					w.WriteHeader(http.StatusNoContent)
				})
				mux := http.NewServeMux()
				mux.Handle(%q, handlers.RequireFields(next))
				mux.Handle(%q, handlers.RequireFields(next))
				path := %q
				if method == http.MethodPut { path += "/1" }
				req := httptest.NewRequest(method, path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				if tc.unknownLength {
					req.ContentLength = -1
					req.TransferEncoding = []string{"chunked"}
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != tc.status { t.Fatalf("status %%d, want %%d: %%s", rec.Code, tc.status, rec.Body) }
				if called != (tc.status == http.StatusNoContent) { t.Fatalf("handler called = %%t", called) }
				if !called && rec.Header().Get("Content-Type") != "application/problem+json" { t.Fatalf("Content-Type = %%q", rec.Header().Get("Content-Type")) }
			})
		}
	}
}
`, rc.spec.Name.PluralPascal(), rc.spec.Name.PluralPascal(), body,
		fmt.Sprintf(`{%q:null}`, strings.ToUpper(first)),
		fmt.Sprintf(`{%q:null,%q:null}`, first, first),
		`{"id":1}`, fmt.Sprintf("{%q:\"\xff\"}", first),
		rc.item, "POST "+base, "PUT "+base+"/{id}", base)
}
