package mcp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/kit/x/apikeys"
)

type pointerText int

func (p *pointerText) MarshalText() ([]byte, error) { return []byte("text"), nil }

type pointerOutput struct {
	Value string `json:"value"`
}

func (*pointerOutput) MarshalJSON() ([]byte, error) { return []byte(`{"value":"custom"}`), nil }

func TestNestedStringSchemas(t *testing.T) {
	type nested struct {
		Date *wireDate `json:"date"`
	}
	type input struct {
		Nested []nested               `json:"nested"`
		IDs    map[string][2]wireUUID `json:"ids"`
		Text   pointerText            `json:"text"`
		Ignore chan struct{}          `json:"-"`
	}
	type output struct {
		ID wireUUID `json:"id"`
	}
	schemas := stringSchemas(reflect.TypeFor[input](), reflect.TypeFor[output]())
	for _, typ := range []reflect.Type{reflect.TypeFor[wireDate](), reflect.TypeFor[wireUUID](), reflect.TypeFor[pointerText]()} {
		if schema := schemas[typ]; schema == nil || schema.Type != "string" {
			t.Errorf("schema for %v = %+v", typ, schema)
		}
	}
	// Registration also checks that nested overrides produce valid schemas.
	s := newServer(t, Config{})
	Tool(s, ToolDef{Name: "nested", Description: "Read nested strings"}, func(context.Context, input) (output, error) { return output{}, nil })
}

func TestSchemaCompatibility(t *testing.T) {
	installKeyReader(t)
	t.Run("nil output pointer", func(t *testing.T) {
		s := newServer(t, Config{})
		Tool(s, ToolDef{Name: "pointer", Description: "Read a pointer"}, func(context.Context, *struct{}) (*testOutput, error) { return nil, nil })
		res, text := callResult(t, s, "pointer")
		if res.IsError || text != `{"value":""}` {
			t.Fatalf("nil pointer output: %+v %q", res, text)
		}
	})
	t.Run("nil named output pointer", func(t *testing.T) {
		type output *testOutput
		s := newServer(t, Config{})
		Tool(s, ToolDef{Name: "pointer", Description: "Read a pointer"}, func(context.Context, struct{}) (output, error) { return nil, nil })
		res, text := callResult(t, s, "pointer")
		if res.IsError || text != `{"value":""}` {
			t.Fatalf("nil named pointer output: %+v %q", res, text)
		}
	})
	t.Run("nil output with pointer marshaler", func(t *testing.T) {
		s := newServer(t, Config{})
		Tool(s, ToolDef{Name: "pointer", Description: "Read a pointer"}, func(context.Context, struct{}) (*pointerOutput, error) { return nil, nil })
		res, text := callResult(t, s, "pointer")
		if res.IsError || text != `{"value":""}` {
			t.Fatalf("nil pointer marshaler output: %+v %q", res, text)
		}
	})
	t.Run("nil output pointer to interface", func(t *testing.T) {
		s := newServer(t, Config{})
		Tool(s, ToolDef{Name: "pointer", Description: "Read a pointer"}, func(context.Context, struct{}) (*any, error) { return nil, nil })
		res, text := callResult(t, s, "pointer")
		if res.IsError || text != `null` {
			t.Fatalf("nil pointer to interface output: %+v %q", res, text)
		}
	})
	t.Run("any input and output", func(t *testing.T) {
		s := newServer(t, Config{})
		Tool(s, ToolDef{Name: "any", Description: "Read any input"}, func(_ context.Context, in any) (any, error) { return in, nil })
		w := rpc(t, route(s), requestContext(apikeys.ScopeWrite, "staff"), "tools/list", `{}`)
		if !strings.Contains(w.Body.String(), `"inputSchema":{"type":"object"}`) || strings.Contains(w.Body.String(), `"outputSchema"`) {
			t.Fatalf("any schemas: %s", w.Body.String())
		}
		res, text := callResult(t, s, "any")
		if res.IsError || text != `{}` {
			t.Fatalf("any output: %+v %q", res, text)
		}
	})
}
