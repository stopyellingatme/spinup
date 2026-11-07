package mcp

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

func toolSchema[T any](schemas map[reflect.Type]*jsonschema.Schema, input bool) any {
	t := reflect.TypeFor[T]()
	if t == reflect.TypeFor[any]() {
		if input {
			return &jsonschema.Schema{Type: "object"}
		}
		return nil
	}
	opts := &jsonschema.ForOptions{TypeSchemas: schemas}
	var schema *jsonschema.Schema
	var err error
	if t.Kind() == reflect.Pointer {
		// The SDK derives the root schema from the pointer's element type.
		schema, err = jsonschema.ForType(t.Elem(), opts)
	} else {
		schema, err = jsonschema.For[T](opts)
	}
	if err != nil {
		panic(fmt.Sprintf("mcp: cannot infer the schema for %v: %v.\nTry: correct the types passed to mcp.Tool.", t, err))
	}
	return schema
}

func stringSchemas(types ...reflect.Type) map[reflect.Type]*jsonschema.Schema {
	schemas := make(map[reflect.Type]*jsonschema.Schema)
	seen := make(map[reflect.Type]bool)
	var visit func(reflect.Type)
	visit = func(t reflect.Type) {
		if seen[t] {
			return
		}
		seen[t] = true
		if t.Kind() != reflect.Pointer && t.Kind() != reflect.String && marshalsString(t) {
			schemas[t] = &jsonschema.Schema{Type: "string"}
			return
		}
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(t.Elem())
		case reflect.Map:
			visit(t.Key())
			visit(t.Elem())
		case reflect.Struct:
			for field := range t.Fields() {
				if (field.IsExported() || field.Anonymous) && strings.Split(field.Tag.Get("json"), ",")[0] != "-" {
					visit(field.Type)
				}
			}
		}
	}
	for _, t := range types {
		visit(t)
	}
	return schemas
}

// marshalsString probes only types that declare a JSON or text marshaler.
// A failed or panicking marshaler leaves the SDK's usual inference unchanged.
func marshalsString(t reflect.Type) (stringValue bool) {
	defer func() { _ = recover() }()
	implements := func(t reflect.Type) bool {
		return t.Implements(reflect.TypeFor[json.Marshaler]()) || t.Implements(reflect.TypeFor[encoding.TextMarshaler]())
	}
	var value any
	switch {
	case implements(t):
		value = reflect.Zero(t).Interface()
	case implements(reflect.PointerTo(t)):
		value = reflect.New(t).Interface()
	default:
		return false
	}
	data, err := json.Marshal(value)
	return err == nil && len(data) > 0 && data[0] == '"'
}
