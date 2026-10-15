package codemod

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestJSON(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{string(data), "{\"z\":1,\"app\":{\"name\":\"old\",\"keep\":true},\"a\":2}", "{\n\t\"z\": 1,\n\t\"app\": {\"name\": \"old\"}\n}\n"} {
		t.Run(src, func(t *testing.T) {
			t.Parallel()
			same, err := SetJSON([]byte(src), []string{"app", "name"}, "old")
			if err != nil || string(same) != src {
				t.Fatal("round trip", err, string(same))
			}
			for _, path := range [][]string{{"app", "name"}, {"app", "extra"}, {"missing", "child"}} {
				out, err := SetJSON([]byte(src), path, "new")
				if err != nil || !json.Valid(out) {
					t.Fatal(err, string(out))
				}
				again, err := SetJSON(out, path, "new")
				if err != nil || !bytes.Equal(again, out) {
					t.Fatal("not idempotent", err)
				}
				if bytes.HasSuffix(out, []byte("\n")) != bytes.HasSuffix([]byte(src), []byte("\n")) {
					t.Fatal("newline changed")
				}
				z := bytes.Index(out, []byte(`"z"`))
				a := bytes.Index(out, []byte(`"app"`))
				if z > a {
					t.Fatal("reordered", string(out))
				}
			}
			for _, path := range [][]string{{"z"}, {"app", "name"}, {"a"}, {"missing"}} {
				out, changed, err := DeleteJSON([]byte(src), path)
				if err != nil || !json.Valid(out) {
					t.Fatal(changed, err, string(out))
				}
				again, changed, err := DeleteJSON(out, path)
				if err != nil || changed || !bytes.Equal(again, out) {
					t.Fatal("delete not idempotent", err)
				}
			}
		})
	}
}

func TestJSONEdges(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"{}", "{ }", "{\n}", "{\r\n}\r\n"} {
		out, err := SetJSON([]byte(src), []string{"a", "b"}, map[string]any{"x": true})
		if err != nil || !json.Valid(out) {
			t.Fatal(err, string(out))
		}
		out, changed, err := DeleteJSON(out, []string{"a"})
		if err != nil || !changed || !json.Valid(out) {
			t.Fatal(err, string(out))
		}
	}
	for _, src := range []string{"[", `{"a":1} {}`, `{"a":1,"a":2}`} {
		if _, err := SetJSON([]byte(src), []string{"a"}, 1); err == nil {
			t.Fatal("accepted", src)
		}
	}
	if _, err := SetJSON([]byte(`{"a":1}`), []string{"a", "b"}, 1); err == nil {
		t.Fatal("overwrote scalar")
	}
}
