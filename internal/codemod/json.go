package codemod

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// SetJSON sets an object path, creating missing objects.
// It keeps existing key order, indentation, and the trailing newline.
// An empty path replaces the root value. Crossing a non-object is an error.
func SetJSON(src []byte, path []string, value any) ([]byte, error) {
	root, err := parseJSON(src)
	if err != nil {
		return nil, editorError("JSON", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, editorError("JSON", err)
	}
	current := root
	for i, key := range path {
		if current.kind != '{' {
			return nil, editorError("JSON", fmt.Errorf("path %q crosses a non-object", path[:i]))
		}
		index := jsonMemberIndex(current, key)
		if index >= 0 {
			current = current.members[index].value
			continue
		}
		// Build the missing suffix in reverse so its order is deterministic.
		for j := len(path) - 1; j > i; j-- {
			name, _ := json.Marshal(path[j])
			encoded = append(append(append([]byte{'{'}, name...), ':'), append(encoded, '}')...)
		}
		return insertJSON(src, current, key, encoded), nil
	}
	old := src[current.start:current.end]
	if equalJSON(old, encoded) {
		return src, nil
	}
	encoded = indentJSON(encoded, jsonLineIndent(src, current.start), jsonIndent(src), newline(src))
	return splice(src, current.start, current.end, encoded), nil
}

// DeleteJSON removes an object entry at path and leaves other entries in order.
// An absent path returns src unchanged. An empty path is an error.
func DeleteJSON(src []byte, path []string) ([]byte, bool, error) {
	root, err := parseJSON(src)
	if err != nil {
		return nil, false, editorError("JSON", err)
	}
	if len(path) == 0 {
		return nil, false, editorError("JSON", fmt.Errorf("cannot delete the document root"))
	}
	parent := root
	for _, key := range path[:len(path)-1] {
		index := jsonMemberIndex(parent, key)
		if index < 0 {
			return src, false, nil
		}
		parent = parent.members[index].value
	}
	index := jsonMemberIndex(parent, path[len(path)-1])
	if index < 0 {
		return src, false, nil
	}
	entry := parent.members[index]
	start, end := entry.start, entry.value.end
	switch {
	case len(parent.members) == 1:
		start = parent.start + 1
		end = parent.end - 1
	case index < len(parent.members)-1:
		end = parent.members[index+1].start
	default:
		start = parent.members[index-1].value.end
	}
	return splice(src, start, end, nil), true, nil
}

type jsonMember struct {
	key   string
	start int
	value *jsonNode
}
type jsonNode struct {
	kind       byte
	start, end int
	members    []jsonMember
}
type jsonParser struct {
	src []byte
	pos int
}

func parseJSON(src []byte) (*jsonNode, error) {
	if !json.Valid(src) {
		return nil, fmt.Errorf("invalid JSON document")
	}
	p := jsonParser{src: src}
	return p.value()
}

// value reads only validated syntax. Token boundaries retain the source bytes.
func (p *jsonParser) value() (*jsonNode, error) {
	p.space()
	n := &jsonNode{kind: p.src[p.pos], start: p.pos}
	switch n.kind {
	case '{':
		p.pos++
		p.space()
		seen := map[string]bool{}
		for p.src[p.pos] != '}' {
			start := p.pos
			keyEnd := p.stringEnd()
			var key string
			if err := json.Unmarshal(p.src[start:keyEnd], &key); err != nil {
				return nil, err
			}
			if seen[key] {
				return nil, fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = true
			p.pos = keyEnd
			p.space()
			p.pos++
			child, err := p.value()
			if err != nil {
				return nil, err
			}
			n.members = append(n.members, jsonMember{key: key, start: start, value: child})
			p.space()
			if p.src[p.pos] == ',' {
				p.pos++
				p.space()
			} else {
				break
			}
		}
		p.pos++
	case '[':
		p.pos++
		p.space()
		for p.src[p.pos] != ']' {
			if _, err := p.value(); err != nil {
				return nil, err
			}
			p.space()
			if p.src[p.pos] == ',' {
				p.pos++
				p.space()
			} else {
				break
			}
		}
		p.pos++
	case '"':
		p.pos = p.stringEnd()
	default:
		for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n,]}", rune(p.src[p.pos])) {
			p.pos++
		}
	}
	n.end = p.pos
	return n, nil
}
func (p *jsonParser) space() {
	for p.pos < len(p.src) && strings.ContainsRune(" \t\r\n", rune(p.src[p.pos])) {
		p.pos++
	}
}
func (p *jsonParser) stringEnd() int {
	for i := p.pos + 1; i < len(p.src); i++ {
		switch p.src[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(p.src)
}
func jsonMemberIndex(n *jsonNode, key string) int {
	for i, m := range n.members {
		if m.key == key {
			return i
		}
	}
	return -1
}
func equalJSON(a, b []byte) bool {
	decode := func(src []byte) any {
		d := json.NewDecoder(bytes.NewReader(src))
		d.UseNumber()
		var v any
		_ = d.Decode(&v)
		return v
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

func insertJSON(src []byte, n *jsonNode, key string, encoded []byte) []byte {
	name, _ := json.Marshal(key)
	indent := jsonIndent(src)
	parentIndent := jsonLineIndent(src, n.start)
	multiline := bytes.Contains(src[n.start:n.end], []byte("\n"))
	if multiline {
		childIndent := parentIndent + indent
		encoded = indentJSON(encoded, childIndent, indent, newline(src))
		entry := append(append(append([]byte(childIndent), name...), []byte(": ")...), encoded...)
		if len(n.members) == 0 {
			return splice(src, n.start+1, n.end-1, append(append([]byte(newline(src)), entry...), []byte(newline(src)+parentIndent)...))
		}
		last := n.members[len(n.members)-1].value.end
		return splice(src, last, last, append([]byte(","+newline(src)), entry...))
	}
	separator := ":"
	comma := ","
	if bytes.Contains(src[n.start:n.end], []byte(": ")) {
		separator = ": "
		comma = ", "
	}
	entry := append(append(name, []byte(separator)...), encoded...)
	if len(n.members) == 0 {
		return splice(src, n.start+1, n.start+1, entry)
	}
	last := n.members[len(n.members)-1].value.end
	return splice(src, last, last, append([]byte(comma), entry...))
}
func jsonLineIndent(src []byte, offset int) string {
	start := lineStart(src, offset)
	end := start
	for end < offset && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}
func jsonIndent(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n")) {
		i := 0
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i > 0 && i < len(line) && line[i] == '"' {
			return string(line[:i])
		}
	}
	return "  "
}
func indentJSON(encoded []byte, prefix, indent, nl string) []byte {
	var out bytes.Buffer
	if err := json.Indent(&out, encoded, prefix, indent); err != nil {
		return encoded
	}
	if nl != "\n" {
		return bytes.ReplaceAll(out.Bytes(), []byte("\n"), []byte(nl))
	}
	return out.Bytes()
}
