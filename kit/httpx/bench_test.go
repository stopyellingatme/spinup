package httpx

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The resource fixture matches the J1 survey's NoteInput (275 encoded bytes).
type benchmarkNoteInput struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Published bool   `json:"published"`
}

func benchmarkBodies(b *testing.B) []struct {
	name string
	data []byte
} {
	b.Helper()
	data := benchmarkSmallJSONBody()
	resource := benchmarkNoteInput{
		Title:     "Measure the JSON API",
		Body:      strings.Repeat("A generated resource body. ", 8),
		Published: true,
	}
	resource.Body += strings.Repeat("x", 64*1024-len(data))
	large, err := json.Marshal(resource)
	if err != nil {
		b.Fatal(err)
	}
	return []struct {
		name string
		data []byte
	}{{"Resource", data}, {"64KiB", large}}
}

func benchmarkSmallJSONBody() []byte {
	data, err := json.Marshal(benchmarkNoteInput{
		Title:     "Measure the JSON API",
		Body:      strings.Repeat("A generated resource body. ", 8),
		Published: true,
	})
	if err != nil {
		panic(err)
	}
	return data
}

func BenchmarkDecodeJSON(b *testing.B) {
	for _, body := range benchmarkBodies(b) {
		for _, method := range []string{"DecodeJSON", "UnmarshalRead", "UnmarshalDecode", "V1"} {
			b.Run(body.name+"/"+method, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body.data)))
				run := benchmarkDecodeJSONBody(body.data, method)
				for b.Loop() {
					run()
				}
			})
		}
	}
}

func BenchmarkCheckJSONBody(b *testing.B) {
	body := benchmarkBodies(b)[0]
	rules := BodyRules{Required: []string{"title", "body", "published"}, Known: []string{"title", "body", "published"}}
	b.ReportAllocs()
	b.SetBytes(int64(len(body.data)))
	run := benchmarkCheckJSONBody(body.data, rules)
	for b.Loop() {
		run()
	}
}

func BenchmarkWriteJSON(b *testing.B) {
	run := benchmarkWriteJSONBody()
	b.ReportAllocs()
	for b.Loop() {
		run()
	}
}

func benchmarkDecodeJSONBody(data []byte, method string) func() {
	return func() {
		r := &http.Request{Body: io.NopCloser(bytes.NewReader(data))}
		var dst benchmarkNoteInput
		var err error
		switch method {
		case "DecodeJSON":
			err = DecodeJSON(r, &dst)
		case "UnmarshalRead":
			err = jsonv2.UnmarshalRead(r.Body, &dst, jsonv2.RejectUnknownMembers(true))
		case "UnmarshalDecode":
			err = benchmarkUnmarshalDecode(r, &dst)
		case "V1":
			err = benchmarkDecodeV1(r, &dst)
		}
		if err != nil {
			panic(err)
		}
	}
}

func benchmarkCheckJSONBody(data []byte, rules BodyRules) func() {
	return func() {
		r := &http.Request{Body: io.NopCloser(bytes.NewReader(data))}
		if err := CheckJSONBody(r, rules); err != nil {
			panic(err)
		}
	}
}

func benchmarkWriteJSONBody() func() {
	return func() {
		w := httptest.NewRecorder()
		if err := WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"}); err != nil {
			panic(err)
		}
	}
}

func benchmarkUnmarshalDecode(r *http.Request, dst any) error {
	d := jsontext.NewDecoder(r.Body)
	if err := jsonv2.UnmarshalDecode(d, dst, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if err := d.SkipValue(); !errors.Is(err, io.EOF) {
		return errors.New("expected EOF")
	}
	return nil
}

func benchmarkDecodeV1(r *http.Request, dst any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected EOF")
	}
	return nil
}
