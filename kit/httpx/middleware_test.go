package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessLogProbeLevel(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz", "/healthz/", "/api/notes"} {
		for _, status := range []int{0, 200, 204, 299, 300, 404, 500, 503} {
			for _, minimum := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
				var out bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: minimum}))
				handler := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if status != 0 {
						w.WriteHeader(status)
					}
				}))
				request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path+"?check=1", nil)
				handler.ServeHTTP(httptest.NewRecorder(), request)
				wantLevel := "INFO"
				if (path == "/healthz" || path == "/readyz") && (status == 0 || status >= 200 && status < 300) {
					wantLevel = "DEBUG"
				}
				if minimum == slog.LevelInfo && wantLevel == "DEBUG" {
					if out.Len() != 0 {
						t.Errorf("%s %d: unexpected record %s", path, status, &out)
					}
					continue
				}
				var record struct {
					Level string `json:"level"`
				}
				if err := json.Unmarshal(out.Bytes(), &record); err != nil {
					t.Fatal(err)
				}
				if record.Level != wantLevel {
					t.Errorf("%s %d: got %s, want %s", path, status, record.Level, wantLevel)
				}
			}
		}
	}
}
