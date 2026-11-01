package httpx

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWriteAttachment(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, filename, contentType string
		wantASCII, wantUTF8         string
		wantType                    string
	}{
		{"plain", "report-42.pdf", "application/pdf", "report-42.pdf", "report-42.pdf", "application/pdf"},
		{"quotes and line breaks", "a\"b'c\r\nSet-Cookie: x=1\n.pdf", "application/pdf", "abcSet-Cookie: x=1.pdf", "abcSet-Cookie: x=1.pdf", "application/pdf"},
		{"path", "../../etc/passwd", "text/plain; charset=utf-8", "etcpasswd", "etcpasswd", "text/plain; charset=utf-8"},
		{"windows path", `C:\Users\x\report.pdf`, "application/pdf", "C:Usersxreport.pdf", "C:Usersxreport.pdf", "application/pdf"},
		{"empty", "", "application/pdf", "download", "download", "application/pdf"},
		{"only hostile", "\"/\\\r\n\t\x00 . ", "application/pdf", "download", "download", "application/pdf"},
		{"unicode", "Bericht März.pdf", "application/pdf", "Bericht M_rz.pdf", "Bericht März.pdf", "application/pdf"},
		{"right-to-left override", "invoice\u202Efdp.exe", "application/pdf", "invoicefdp.exe", "invoicefdp.exe", "application/pdf"},
		{"separators", "a\u2028b\u2029c%20d;e,f.pdf", "application/pdf", "abc_20d_e_f.pdf", "abc%20d;e,f.pdf", "application/pdf"},
		{"bad type", "x.bin", "not a type\r\nX-Evil: 1", "x.bin", "x.bin", "application/octet-stream"},
		{"empty type", "x.bin", "", "x.bin", "x.bin", "application/octet-stream"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			body := []byte("%PDF-1.7 body")
			WriteAttachment(rec, tt.filename, tt.contentType, body)
			res := rec.Result()
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusOK || rec.Body.String() != string(body) {
				t.Fatalf("status %d body %q", res.StatusCode, rec.Body.String())
			}
			for k, want := range map[string]string{
				"Content-Type":           tt.wantType,
				"Content-Length":         "13",
				"Cache-Control":          "private, no-store",
				"X-Content-Type-Options": "nosniff",
			} {
				if got := res.Header.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			cd := res.Header.Get("Content-Disposition")
			if strings.ContainsAny(cd, "\r\n") || len(res.Header.Values("Set-Cookie")) != 0 || res.Header.Get("X-Evil") != "" {
				t.Fatalf("Content-Disposition %q injects a header", cd)
			}
			disposition, params, err := mime.ParseMediaType(cd)
			if err != nil {
				t.Fatalf("parse %q: %v", cd, err)
			}
			if disposition != "attachment" {
				t.Errorf("disposition = %q", disposition)
			}
			// mime decodes filename* into filename when it is present.
			if params["filename"] != tt.wantUTF8 {
				t.Errorf("decoded filename = %q, want %q (header %q)", params["filename"], tt.wantUTF8, cd)
			}
			if !strings.Contains(cd, `filename="`+tt.wantASCII+`"`) {
				t.Errorf("header %q has no ASCII filename %q", cd, tt.wantASCII)
			}
			i := strings.Index(cd, "filename*=UTF-8''")
			if i < 0 {
				t.Fatalf("header %q has no filename*", cd)
			}
			encoded := cd[i+len("filename*=UTF-8''"):]
			if dec, err := url.PathUnescape(encoded); err != nil || dec != tt.wantUTF8 {
				t.Errorf("filename* %q decodes to %q, %v", encoded, dec, err)
			}
			for _, r := range cd {
				if r > 0x7e || r < 0x20 {
					t.Fatalf("header %q has the byte %q", cd, r)
				}
			}
		})
	}
}

func TestCleanFilenameLimit(t *testing.T) {
	t.Parallel()
	got := cleanFilename(strings.Repeat("ä", 300) + ".pdf")
	if len(got) > maxFilenameBytes || !strings.HasPrefix(got, "ää") {
		t.Fatalf("len %d: %q", len(got), got)
	}
	if got := cleanFilename("\xff\xfereport.pdf"); got != "report.pdf" {
		t.Fatalf("invalid UTF-8 = %q", got)
	}
}
