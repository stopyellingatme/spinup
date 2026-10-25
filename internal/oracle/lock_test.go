package oracle

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
)

type lockCase struct {
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Expected string          `json:"expected"`
}

func jsonBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func lockCases(t *testing.T) []lockCase {
	t.Helper()
	locks := []struct {
		name string
		lock *manifest.Lock
	}{{"nil collections", manifest.New()}, {"empty collections", manifest.New()}, {"html and unicode", manifest.New()}, {"number precision", manifest.New()}, {"collection order", manifest.New()}, {"nested values and file fields", manifest.New()}}
	locks[1].lock.Packs = []string{}
	locks[1].lock.Plugins = []manifest.Plugin{}
	locks[1].lock.Tombstones = []string{}
	locks[1].lock.Orphans = []string{}
	locks[1].lock.AppliedMigrations = []string{}
	locks[2].lock.Answers = map[string]any{"z": "<>&", "a": "line\u2028paragraph\u2029é界😀\b\f\n\r\t\x00"}
	locks[3].lock.Answers = map[string]any{}
	for i, number := range []string{"9007199254740993", "18446744073709551616", "1234567890123456789012345678901234567890", "-0", "1.2300", "1e+30", "1E-30", "0.000000000000000000001"} {
		locks[3].lock.Answers[strconv.Itoa(i)] = json.Number(number)
	}
	ordered := locks[4].lock
	ordered.Packs = []string{"web", "base", "api"}
	ordered.Plugins = []manifest.Plugin{{Name: "z", Version: "1"}, {Name: "a", Version: "2", API: 2}, {Name: "a", Version: "1", API: 1}, {Name: "a", Version: "1", API: 0}}
	ordered.Tombstones = []string{"z.txt", "a.txt", "z.txt"}
	ordered.Orphans = []string{"z.txt", "a.txt", "a.txt"}
	ordered.AppliedMigrations = []string{"0002", "0001", "0002"}
	nested := locks[5].lock
	nested.Answers = map[string]any{"null": nil, "empty": []any{}, "list": []any{nil, true, false, map[string]any{"z": 0, "a": "&"}}, "object": map[string]any{}}
	nested.Files["z.txt"] = manifest.FileEntry{Class: manifest.Seed}
	nested.Files["a.txt"] = manifest.FileEntry{Class: manifest.Managed, Template: "base/a.tmpl", Hash: "sha256:" + strings.Repeat("a", 64), Mode: "0644"}
	var cases []lockCase
	for _, item := range locks {
		input, err := json.Marshal(item.lock)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := item.lock.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := manifest.Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		roundtrip, err := parsed.Marshal()
		if err != nil || string(roundtrip) != string(expected) {
			t.Fatalf("Go lock roundtrip %s: %v", item.name, err)
		}
		cases = append(cases, lockCase{item.name, input, string(expected)})
	}
	return cases
}

type quoteCase struct {
	Input    string `json:"input"`
	Expected string `json:"expected"`
}

func quoteCases() []quoteCase {
	values := []string{"", "hello", "<>&", "\"\\", "\a\b\f\n\r\t\v", "é界😀", "\u2028\u2029", "\u00a0\u0085\u007f", "\ufffd", "\u200b\ufeff", "'`$"}
	for i := 0; i < 256; i++ {
		values = append(values, string(rune(i)))
	}
	var cases []quoteCase
	for _, value := range values {
		cases = append(cases, quoteCase{value, strconv.Quote(value)})
	}
	return cases
}
