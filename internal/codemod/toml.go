package codemod

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// SetTOML replaces a value without rewriting unrelated bytes.
// A missing key is appended to its table. A missing table is appended to the file.
// Supported values are string, bool, int64, float64, and []string.
func SetTOML(src []byte, table []string, key string, value any) ([]byte, error) {
	encoded, err := tomlValue(value)
	if err != nil {
		return nil, editorError("TOML", err)
	}
	doc, location, err := locateTOML(src, table, key)
	if err != nil {
		return nil, editorError("TOML", err)
	}
	if existing, ok := tomlLookup(doc, append(slices.Clone(table), key)); ok && equalTOML(existing, value) {
		return src, nil
	}
	var out []byte
	if location.found {
		replacement := append([]byte(nil), encoded...)
		// Comments inside a replaced array become standalone comments before its key.
		if len(location.comments) > 0 {
			prefix := []byte(strings.Join(location.comments, newline(src)) + newline(src))
			out = splice(src, location.keyStart, location.valueEnd, append(prefix, append(append([]byte(nil), src[location.keyStart:location.valueStart]...), replacement...)...))
		} else {
			out = splice(src, location.valueStart, location.valueEnd, replacement)
		}
	} else {
		insertion := []byte(tomlKey(key) + " = " + string(encoded) + newline(src))
		offset := location.tableEnd
		if !location.tableFound {
			offset = len(src)
			var names []string
			for _, name := range table {
				names = append(names, tomlKey(name))
			}
			insertion = append([]byte("["+strings.Join(names, ".")+"]"+newline(src)), insertion...)
		}
		if offset > 0 && src[offset-1] != '\n' {
			insertion = append([]byte(newline(src)), insertion...)
		}
		out = splice(src, offset, offset, insertion)
	}
	if _, _, err := locateTOML(out, table, key); err != nil {
		return nil, editorError("TOML", err)
	}
	return out, nil
}

// DeleteTOML removes an entry and preserves its comments.
// If the key is absent, it returns src unchanged.
func DeleteTOML(src []byte, table []string, key string) ([]byte, bool, error) {
	_, loc, err := locateTOML(src, table, key)
	if err != nil {
		return nil, false, editorError("TOML", err)
	}
	if !loc.found {
		return src, false, nil
	}
	replacement := []byte(nil)
	if len(loc.comments) > 0 {
		replacement = []byte(strings.Join(loc.comments, newline(src)) + newline(src))
	}
	start, end := loc.keyStart, loc.valueEnd
	// Remove an empty assignment line. An inline comment stays on its own line.
	lineEnd := len(src)
	if i := bytes.IndexByte(src[end:], '\n'); i >= 0 {
		lineEnd = end + i + 1
	}
	if len(bytes.TrimSpace(src[end:lineEnd])) == 0 {
		end = lineEnd
		start = lineStart(src, start)
	}
	out := splice(src, start, end, replacement)
	return out, true, nil
}

type tomlLocation struct {
	found, tableFound                        bool
	keyStart, valueStart, valueEnd, tableEnd int
	comments                                 []string
}

func locateTOML(src []byte, table []string, key string) (map[string]any, tomlLocation, error) {
	var doc map[string]any
	if err := toml.Unmarshal(src, &doc); err != nil {
		return nil, tomlLocation{}, err
	}
	loc := tomlLocation{tableFound: len(table) == 0, tableEnd: len(src)}
	current := []string(nil)
	currentArray := false
	active := len(table) == 0
	target := append(slices.Clone(table), key)
	p := unstable.Parser{KeepComments: true}
	p.Reset(src)
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			keys := tomlNodeKey(n)
			it := n.Key()
			it.Next()
			header := lineStart(src, int(it.Node().Raw.Offset))
			if active && loc.tableEnd == len(src) {
				loc.tableEnd = header
			}
			active = false
			current = keys
			currentArray = n.Kind == unstable.ArrayTable
			if slices.Equal(current, table) {
				if currentArray {
					return nil, loc, fmt.Errorf("table %q is an array of tables", table)
				}
				loc.tableFound = true
				active = true
				loc.tableEnd = len(src)
			}
		case unstable.KeyValue:
			if currentArray {
				continue
			}
			if active && slices.Equal(current, table) {
				loc.tableEnd = lineEnd(src, int(n.Raw.Offset+n.Raw.Length))
			}
			full := append(slices.Clone(current), tomlNodeKey(n)...)
			if !slices.Equal(full, target) {
				continue
			}
			loc.found = true
			loc.keyStart = int(n.Raw.Offset)
			loc.valueEnd = int(n.Raw.Offset + n.Raw.Length)
			keys := n.Key()
			lastEnd := loc.keyStart
			for keys.Next() {
				raw := keys.Node().Raw
				lastEnd = int(raw.Offset + raw.Length)
			}
			equals := bytes.IndexByte(src[lastEnd:loc.valueEnd], '=')
			if equals < 0 {
				return nil, loc, fmt.Errorf("key %q has no assignment", key)
			}
			loc.valueStart = lastEnd + equals + 1
			for loc.valueStart < loc.valueEnd && (src[loc.valueStart] == ' ' || src[loc.valueStart] == '\t') {
				loc.valueStart++
			}
			loc.comments = tomlComments(n.Value(), loc.valueStart, loc.valueEnd)
		}
	}
	if err := p.Error(); err != nil {
		return nil, loc, err
	}
	return doc, loc, nil
}

func tomlNodeKey(n *unstable.Node) []string {
	var keys []string
	it := n.Key()
	for it.Next() {
		keys = append(keys, string(it.Node().Data))
	}
	return keys
}
func tomlComments(n *unstable.Node, start, end int) []string {
	var comments []string
	if n.Kind == unstable.Comment && int(n.Raw.Offset) >= start && int(n.Raw.Offset+n.Raw.Length) <= end {
		comments = append(comments, string(n.Data))
	}
	it := n.Children()
	for it.Next() {
		comments = append(comments, tomlComments(it.Node(), start, end)...)
	}
	return comments
}
func tomlLookup(doc map[string]any, path []string) (any, bool) {
	var current any = doc
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
func tomlValue(value any) ([]byte, error) {
	switch value.(type) {
	case string, bool, int64, float64, []string:
	default:
		return nil, fmt.Errorf("unsupported value type %T", value)
	}
	encoded, err := toml.Marshal(map[string]any{"value": value})
	if err != nil {
		return nil, err
	}
	_, raw, _ := bytes.Cut(encoded, []byte("="))
	return bytes.TrimSpace(raw), nil
}

// tomlKey writes a bare key when TOML allows one, else a quoted key.
func tomlKey(key string) string {
	if bareKey.MatchString(key) {
		return key
	}
	encoded, _ := json.Marshal(key)
	return string(encoded)
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func newline(src []byte) string {
	if bytes.Contains(src, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}
func lineStart(src []byte, offset int) int { return bytes.LastIndexByte(src[:offset], '\n') + 1 }
func lineEnd(src []byte, offset int) int {
	if end := bytes.IndexByte(src[offset:], '\n'); end >= 0 {
		return offset + end + 1
	}
	return len(src)
}
func splice(src []byte, start, end int, value []byte) []byte {
	out := make([]byte, 0, len(src)-end+start+len(value))
	out = append(out, src[:start]...)
	out = append(out, value...)
	return append(out, src[end:]...)
}

func equalTOML(existing, value any) bool {
	if want, ok := value.([]string); ok {
		got, ok := existing.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for i, item := range want {
			if got[i] != item {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(existing, value)
}
