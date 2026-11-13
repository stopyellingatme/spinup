package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fuzzBodyLimit = 1024

var jsonFuzzSeeds = [][]byte{
	[]byte(`{"title":"valid"}`),
	[]byte(`{"title":"first","title":"second"}`),
	[]byte(`{"unknown":true}`),
	[]byte(`{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":true}}}}}}}}`),
	[]byte(`{"number":12345678901234567890123456789012345678901234567890}`),
	[]byte("{\"title\":\"\xff\"}"),
	[]byte(`{} {}`),
	[]byte(""),
	[]byte(`null`),
	[]byte(`[]`),
	[]byte(`{"title":"` + strings.Repeat("x", fuzzBodyLimit) + `"}`),
	[]byte("\xef\xbb\xbf{}"),
}

// FuzzDecodeJSON checks strict JSON decoding against malformed request bodies.
func FuzzDecodeJSON(f *testing.F) {
	for _, seed := range jsonFuzzSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		assertFuzzDecodeStatus(t, decodeFuzzObject(body))
		assertFuzzDecodeStatus(t, decodeFuzzMap(body))
	})
}

// FuzzCheckJSONBody checks JSON body validation and body restoration.
func FuzzCheckJSONBody(f *testing.F) {
	for _, seed := range jsonFuzzSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		objectErr := decodeFuzzObject(body)
		assertFuzzDecodeStatus(t, objectErr)
		mapErr := decodeFuzzMap(body)
		assertFuzzDecodeStatus(t, mapErr)

		restored, checkErr := checkFuzzBody(body)
		assertFuzzCheckStatus(t, checkErr)
		if checkErr == nil && !bytes.Equal(restored, body) {
			t.Fatalf("restored body %q, want %q", restored, body)
		}
		if mapErr == nil && problemStatus(checkErr) == http.StatusBadRequest {
			t.Fatalf("CheckJSONBody returned 400 for DecodeJSON input %q", body)
		}
		if bytes.Equal(body, jsonFuzzSeeds[1]) {
			if problemStatus(mapErr) != http.StatusBadRequest {
				t.Fatalf("DecodeJSON accepted duplicate key body %q", body)
			}
			if problemStatus(checkErr) != http.StatusBadRequest {
				t.Fatalf("CheckJSONBody accepted duplicate key body %q", body)
			}
		}
	})
}

func decodeFuzzObject(body []byte) error {
	r := fuzzRequest(body)
	var dst struct {
		Title string `json:"title"`
	}
	return DecodeJSON(r, &dst)
}

func decodeFuzzMap(body []byte) error {
	r := fuzzRequest(body)
	var dst map[string]any
	return DecodeJSON(r, &dst)
}

func checkFuzzBody(body []byte) ([]byte, error) {
	r := fuzzRequest(body)
	err := CheckJSONBody(r, BodyRules{})
	if err != nil {
		return nil, err
	}
	restored, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		return nil, readErr
	}
	return restored, nil
}

func fuzzRequest(body []byte) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", bytes.NewReader(body))
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, fuzzBodyLimit)
	return r
}

func assertFuzzDecodeStatus(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	status := problemStatus(err)
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge && status != http.StatusUnprocessableEntity {
		t.Fatalf("DecodeJSON error status %d for input %q", status, err)
	}
}

func assertFuzzCheckStatus(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	status := problemStatus(err)
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge && status != http.StatusUnprocessableEntity {
		t.Fatalf("CheckJSONBody error status %d for input %q", status, err)
	}
}

func problemStatus(err error) int {
	var problem *Error
	if errors.As(err, &problem) && problem != nil {
		return problem.Problem.Status
	}
	return 0
}
