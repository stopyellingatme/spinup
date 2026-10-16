package resource

import (
	"fmt"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen/fields"
)

// yamlWriter writes indented YAML lines.
type yamlWriter struct{ b strings.Builder }

func (w *yamlWriter) line(indent int, format string, args ...any) {
	w.b.WriteString(strings.Repeat("  ", indent))
	fmt.Fprintf(&w.b, format, args...)
	w.b.WriteByte('\n')
}

// property writes the schema of one field at indent.
func (w *yamlWriter) property(indent int, f fields.Field) {
	w.line(indent, "%s:", f.Column())
	if f.Type.Name == "json" {
		w.line(indent+1, "type: object")
		w.line(indent+1, "additionalProperties: true")
	} else {
		w.line(indent+1, "type: %s", f.Type.OpenAPIType)
	}
	if f.Type.OpenAPIFormat != "" {
		w.line(indent+1, "format: %s", f.Type.OpenAPIFormat)
	}
	if len(f.Type.Enum) > 0 {
		w.line(indent+1, "enum:")
		for _, v := range f.Type.Enum {
			w.line(indent+2, "- %s", v)
		}
	}
	if f.Nullable {
		w.line(indent+1, "nullable: true")
	}
	if f.Type.Ref != "" {
		parent, _ := fields.ParseName(f.Type.Ref)
		w.line(indent+1, "description: The id of a row in %s.", parent.Table())
	}
}

func (w *yamlWriter) required(indent int, names []string) {
	if len(names) == 0 {
		return
	}
	w.line(indent, "required:")
	for _, n := range names {
		w.line(indent+1, "- %s", n)
	}
}

// schemasYAML renders api/openapi/schemas/<singular>.yaml: the resource, its
// input, and its list page.
func schemasYAML(spec Spec) []byte {
	name := spec.Name.Pascal()
	var w yamlWriter
	w.line(0, "# Schemas of the %s resource. spinup bundles them into api/openapi/bundle.gen.yaml.", spec.Name.Snake())

	w.line(0, "%s:", name)
	w.line(1, "type: object")
	all := []string{"id"}
	for _, f := range spec.Fields {
		all = append(all, f.Column())
	}
	w.required(1, append(all, "created_at", "updated_at"))
	w.line(1, "properties:")
	w.line(2, "id:")
	w.line(3, "type: integer")
	w.line(3, "format: int64")
	for _, f := range spec.Fields {
		w.property(2, f)
	}
	for _, ts := range []string{"created_at", "updated_at"} {
		w.line(2, "%s:", ts)
		w.line(3, "type: string")
		w.line(3, "format: date-time")
	}

	w.line(0, "%sInput:", name)
	w.line(1, "type: object")
	var required []string
	for _, f := range spec.Fields {
		if !f.Nullable {
			required = append(required, f.Column())
		}
	}
	w.required(1, required)
	w.line(1, "properties:")
	for _, f := range spec.Fields {
		w.property(2, f)
	}

	w.line(0, "%sList:", name)
	w.line(1, "type: object")
	w.required(1, []string{"items", "total"})
	w.line(1, "properties:")
	w.line(2, "items:")
	w.line(3, "type: array")
	w.line(3, "items:")
	w.line(4, "$ref: \"#/components/schemas/%s\"", name)
	w.line(2, "total:")
	w.line(3, "type: integer")
	w.line(3, "format: int64")
	return []byte(w.b.String())
}

// response writes one response of an operation.
func (w *yamlWriter) response(code, description, mediaType, schema string) {
	const indent = 3
	w.line(indent, "%q:", code)
	w.line(indent+1, "description: %s", description)
	if schema == "" {
		return
	}
	w.line(indent+1, "content:")
	w.line(indent+2, "%s:", mediaType)
	w.line(indent+3, "schema:")
	w.line(indent+4, "$ref: \"#/components/schemas/%s\"", schema)
}

// problems writes the problem responses of an operation.
func (w *yamlWriter) problems(words string, codes ...string) {
	const indent = 3
	descriptions := map[string]string{
		"400": "The request is malformed.",
		"401": "A signed-in user is required.",
		"404": "The " + words + " does not exist.",
		"409": "The request conflicts with other data.",
		"422": "The request has invalid values.",
	}
	for _, code := range codes {
		w.response(code, descriptions[code], "application/problem+json", "Problem")
	}
	w.line(indent, "default:")
	w.line(indent+1, "description: An unexpected error.")
	w.line(indent+1, "content:")
	w.line(indent+2, "application/problem+json:")
	w.line(indent+3, "schema:")
	w.line(indent+4, "$ref: \"#/components/schemas/Problem\"")
}

func (w *yamlWriter) body(indent int, schema string) {
	w.line(indent, "requestBody:")
	w.line(indent+1, "required: true")
	w.line(indent+1, "content:")
	w.line(indent+2, "application/json:")
	w.line(indent+3, "schema:")
	w.line(indent+4, "$ref: \"#/components/schemas/%s\"", schema)
}

// pathsYAML renders api/openapi/paths/<plural>.yaml.
func pathsYAML(spec Spec) []byte {
	n := spec.Name
	singular, plural := n.Pascal(), n.PluralPascal()
	words := strings.ReplaceAll(n.Snake(), "_", " ")
	pluralWords := strings.ReplaceAll(n.PluralSnake(), "_", " ")
	var w yamlWriter
	problems := func(words string, codes ...string) {
		if spec.Owned || spec.Shared {
			codes = append(codes, "401")
		}
		w.problems(words, codes...)
	}
	w.line(0, "# Operations of the %s resource. spinup bundles them into api/openapi/bundle.gen.yaml.", n.Snake())

	w.line(0, "/api/%s:", n.PluralSnake())
	w.line(1, "get:")
	w.line(2, "operationId: list%s", plural)
	w.line(2, "summary: List %s", pluralWords)
	w.line(2, "tags: [%s]", n.PluralSnake())
	w.line(2, "parameters:")
	w.line(3, "- name: limit")
	w.line(4, "in: query")
	w.line(4, "description: The number of %s to return.", pluralWords)
	w.line(4, "schema:")
	w.line(5, "type: integer")
	w.line(5, "format: int32")
	w.line(5, "minimum: 1")
	w.line(5, "maximum: 200")
	w.line(5, "default: 50")
	w.line(3, "- name: offset")
	w.line(4, "in: query")
	w.line(4, "description: The number of %s to skip.", pluralWords)
	w.line(4, "schema:")
	w.line(5, "type: integer")
	w.line(5, "format: int32")
	w.line(5, "minimum: 0")
	w.line(5, "default: 0")
	w.line(2, "responses:")
	w.response("200", fmt.Sprintf("A page of %s in id order.", pluralWords), "application/json", singular+"List")
	problems(words, "400", "422")

	w.line(1, "post:")
	w.line(2, "operationId: create%s", singular)
	w.line(2, "summary: Create a %s", words)
	w.line(2, "tags: [%s]", n.PluralSnake())
	w.body(2, singular+"Input")
	w.line(2, "responses:")
	w.response("201", fmt.Sprintf("The new %s.", words), "application/json", singular)
	problems(words, "400", "409", "422")

	w.line(0, "/api/%s/{id}:", n.PluralSnake())
	w.line(1, "parameters:")
	w.line(2, "- name: id")
	w.line(3, "in: path")
	w.line(3, "required: true")
	w.line(3, "schema:")
	w.line(4, "type: integer")
	w.line(4, "format: int64")
	w.line(1, "get:")
	w.line(2, "operationId: get%s", singular)
	w.line(2, "summary: Get a %s", words)
	w.line(2, "tags: [%s]", n.PluralSnake())
	w.line(2, "responses:")
	w.response("200", fmt.Sprintf("The %s.", words), "application/json", singular)
	problems(words, "400", "404")

	w.line(1, "put:")
	w.line(2, "operationId: update%s", singular)
	w.line(2, "summary: Replace a %s", words)
	w.line(2, "tags: [%s]", n.PluralSnake())
	w.body(2, singular+"Input")
	w.line(2, "responses:")
	w.response("200", fmt.Sprintf("The updated %s.", words), "application/json", singular)
	problems(words, "400", "404", "409", "422")

	w.line(1, "delete:")
	w.line(2, "operationId: delete%s", singular)
	w.line(2, "summary: Delete a %s", words)
	w.line(2, "tags: [%s]", n.PluralSnake())
	w.line(2, "responses:")
	w.response("204", fmt.Sprintf("The %s is deleted.", words), "", "")
	problems(words, "400", "404", "409")
	return []byte(w.b.String())
}
