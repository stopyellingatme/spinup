package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
)

type clash struct {
	Path  string `json:"path"`
	Token string `json:"token"`
	Line  int    `json:"line"`
}

type converter struct {
	dot          string
	serial       int
	err          error
	name, source string
	clashes      []clash
}

func functions() template.FuncMap {
	m := template.FuncMap{}
	for _, name := range strings.Fields("lower upper snake kebab camel pascal quote join indent default has image npm gomod uses") {
		m[name] = func(...any) string { return "" }
	}
	return m
}

func convert(name, source, left, right string) (string, []clash, error) {
	t, err := template.New(name).Delims(left, right).Funcs(functions()).Parse(source)
	if err != nil {
		return "", nil, err
	}
	if len(t.Templates()) != 1 {
		return "", nil, fmt.Errorf("%s: named templates are unsupported", name)
	}
	c := &converter{dot: "data", name: name, source: source}

	// A literal "[" before an action would create a new "[[" marker.
	marker := "[" + left
	for offset := 0; offset < len(source); {
		i := strings.Index(source[offset:], marker)
		if i < 0 {
			break
		}
		c.clashes = append(c.clashes, clash{name, "[ before action", strings.Count(source[:offset+i], "\n") + 1})
		offset += i + len(marker)
	}
	out := c.list(t.Root)
	return out, c.clashes, c.err
}

func literal(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func block(s string) string  { return "[% " + s + " %]" }
func output(s string) string { return "[[ " + s + " ]]" }

func (c *converter) fail(n parse.Node) string {
	if c.err == nil {
		c.err = fmt.Errorf("%s: unsupported %T: %s", c.name, n, n)
	}
	return ""
}

func (c *converter) list(list *parse.ListNode) string {
	if list == nil {
		return ""
	}
	var b strings.Builder
	for _, n := range list.Nodes {
		switch n := n.(type) {
		case *parse.TextNode:
			s := string(n.Text)
			for _, token := range []string{"[[", "]]", "[%", "[#"} {
				for offset := 0; offset < len(s); {
					i := strings.Index(s[offset:], token)
					if i < 0 {
						break
					}
					pos := min(int(n.Position())+offset+i, len(c.source))
					c.clashes = append(c.clashes, clash{c.name, token, strings.Count(c.source[:max(pos, 0)], "\n") + 1})
					offset += i + len(token)
				}
			}
			// Escape opening markers. Closing markers in text are inert.
			for len(s) > 0 {
				i := strings.Index(s, "[")
				if i < 0 {
					b.WriteString(s)
					break
				}
				b.WriteString(s[:i])
				switch {
				case i+1 == len(s):
					b.WriteString(output(literal("[")))
					s = ""
				case strings.ContainsRune("[%#", rune(s[i+1])):
					b.WriteString(output(literal(s[i : i+2])))
					s = s[i+2:]
				default:
					b.WriteByte('[')
					s = s[i+1:]
				}
			}
		case *parse.ActionNode:
			if len(n.Pipe.Decl) > 0 {
				if len(n.Pipe.Decl) != 1 {
					c.fail(n)
					break
				}
				b.WriteString(block("set " + c.expr(n.Pipe.Decl[0]) + " = " + c.pipe(n.Pipe)))
			} else {
				b.WriteString(output(c.pipe(n.Pipe)))
			}
		case *parse.IfNode:
			if len(n.Pipe.Decl) != 0 {
				c.fail(n)
				break
			}
			b.WriteString(block("if " + c.pipe(n.Pipe)))
			b.WriteString(c.list(n.List))
			if n.ElseList != nil {
				b.WriteString(block("else"))
				b.WriteString(c.list(n.ElseList))
			}
			b.WriteString(block("endif"))
		case *parse.WithNode:
			if len(n.Pipe.Decl) != 0 {
				c.fail(n)
				break
			}
			c.serial++
			value := fmt.Sprintf("_with%d", c.serial)
			b.WriteString(block("set " + value + " = " + c.pipe(n.Pipe)))
			b.WriteString(block("if " + value))
			old := c.dot
			c.dot = value
			b.WriteString(c.list(n.List))
			c.dot = old
			if n.ElseList != nil {
				b.WriteString(block("else"))
				b.WriteString(c.list(n.ElseList))
			}
			b.WriteString(block("endif"))
		case *parse.RangeNode:
			c.serial++
			item := fmt.Sprintf("_item%d", c.serial)
			b.WriteString(block("for " + item + " in go_items(" + c.pipe(n.Pipe) + ")"))
			for i, d := range n.Pipe.Decl {
				part := "1"
				if len(n.Pipe.Decl) == 2 && i == 0 {
					part = "0"
				}
				b.WriteString(block("set " + c.expr(d) + " = " + item + "[" + part + "]"))
			}
			old := c.dot
			c.dot = item + "[1]"
			b.WriteString(c.list(n.List))
			c.dot = old
			if n.ElseList != nil {
				b.WriteString(block("else"))
				b.WriteString(c.list(n.ElseList))
			}
			b.WriteString(block("endfor"))
		case *parse.BreakNode:
			b.WriteString(block("break"))
		case *parse.ContinueNode:
			b.WriteString(block("continue"))
		default:
			c.fail(n)
		}
	}
	return b.String()
}

func (c *converter) expr(n parse.Node) string {
	switch n := n.(type) {
	case *parse.DotNode:
		return c.dot
	case *parse.FieldNode:
		return c.dot + "." + strings.Join(n.Ident, ".")
	case *parse.VariableNode:
		base := "data"
		if n.Ident[0] != "$" {
			base = "v_" + strings.TrimPrefix(n.Ident[0], "$")
		}
		if len(n.Ident) > 1 {
			base += "." + strings.Join(n.Ident[1:], ".")
		}
		return base
	case *parse.StringNode:
		for _, token := range []string{"[[", "]]"} {
			for count := strings.Count(n.Text, token); count > 0; count-- {
				pos := min(max(int(n.Position()), 0), len(c.source))
				c.clashes = append(c.clashes, clash{c.name, token, strings.Count(c.source[:pos], "\n") + 1})
			}
		}
		return literal(n.Text)
	case *parse.BoolNode:
		return strconv.FormatBool(n.True)
	case *parse.NumberNode:
		return n.Text
	case *parse.NilNode:
		return "none"
	case *parse.PipeNode:
		return "(" + c.pipe(n) + ")"
	case *parse.ChainNode:
		return "(" + c.expr(n.Node) + ")." + strings.Join(n.Field, ".")
	default:
		return c.fail(n)
	}
}

func (c *converter) pipe(p *parse.PipeNode) string {
	previous := ""
	for _, cmd := range p.Cmds {
		var args []string
		for _, arg := range cmd.Args[1:] {
			args = append(args, c.expr(arg))
		}
		if previous != "" {
			args = append(args, previous)
		}
		if id, ok := cmd.Args[0].(*parse.IdentifierNode); ok {
			switch id.Ident {
			case "and", "or":
				previous = "(" + strings.Join(args, " "+id.Ident+" ") + ")"
			case "not":
				previous = "(not " + strings.Join(args, "") + ")"
			case "eq", "ne", "lt", "le", "gt", "ge":
				op := map[string]string{"eq": "==", "ne": "!=", "lt": "<", "le": "<=", "gt": ">", "ge": ">="}[id.Ident]
				if len(args) != 2 {
					return c.fail(cmd)
				}
				previous = "(" + args[0] + " " + op + " " + args[1] + ")"
			case "index":
				previous = args[0]
				for _, a := range args[1:] {
					previous = "get(" + previous + ", " + a + ")"
				}
			default:
				name := id.Ident
				if name == "default" || name == "join" || name == "len" || name == "printf" {
					name = "go_" + name
				}
				if name == "indent" && strings.HasPrefix(c.name, "_inline/resource/") {
					name = "page_indent"
				}
				previous = name + "(" + strings.Join(args, ", ") + ")"
			}
		} else if len(cmd.Args) == 1 && previous == "" {
			previous = c.expr(cmd.Args[0])
		} else {
			return c.fail(cmd)
		}
	}
	return previous
}
