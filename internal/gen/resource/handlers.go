package resource

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen/fields"
)

// field holds the names of one field in each generated layer.
type field struct {
	fields.Field
	// col is the SQL column and the JSON property.
	col string
	// store is the Go field name in the sqlc structs.
	store string
	// api is the Go field name in the oapi-codegen structs.
	api string
}

// resourceCode holds the names that the Go files of one resource use.
type resourceCode struct {
	spec   Spec
	module string
	fields []field
	// row is the sqlc model struct, such as "Todo".
	row string
	// item is the API schema type, such as "Todo".
	item string
	// conv is the lower camel case prefix of the conversion functions.
	conv string
}

func newResourceCode(spec Spec, module string) resourceCode {
	rc := resourceCode{
		spec:   spec,
		module: module,
		row:    storeStruct(spec.Name.Table()),
		item:   spec.Name.Pascal(),
		conv:   spec.Name.Camel(),
	}
	for _, f := range spec.Fields {
		rc.fields = append(rc.fields, field{Field: f, col: f.Column(), store: storeName(f.Column()), api: apiName(f.Column())})
	}
	return rc
}

func (rc resourceCode) imports() []string {
	return []string{
		rc.module + "/db/migrations",
		rc.module + "/internal/gen",
		rc.module + "/internal/handlers",
		rc.module + "/internal/routes",
		rc.module + "/internal/store",
	}
}

// pgtypeOf returns the pgtype struct and its value field of a nullable column
// that sqlc maps to a pgtype value, or "" for other columns.
func pgtypeOf(f fields.Field) (typ, value string) {
	switch f.Type.Name {
	case "string", "text", "enum":
		return "Text", "String"
	case "int":
		return "Int4", "Int32"
	case "bigint", "ref":
		return "Int8", "Int64"
	case "bool":
		return "Bool", "Bool"
	case "float":
		return "Float8", "Float64"
	case "decimal":
		return "Numeric", ""
	}
	return "", ""
}

// storeType returns the Go type that sqlc uses for the column of f (with the
// overrides in api/sqlc.yaml).
func storeType(f fields.Field) string {
	if f.Nullable {
		if typ, _ := pgtypeOf(f); typ != "" {
			return "pgtype." + typ
		}
		switch f.Type.Name {
		case "time", "date":
			return "*time.Time"
		case "uuid":
			return "*uuid.UUID"
		}
	}
	switch f.Type.Name {
	case "date":
		return "time.Time"
	case "enum":
		return "string"
	case "ref":
		return "int64"
	}
	return f.Type.GoStore
}

// enumType returns the oapi-codegen type of an enum property in schema.
func enumType(schema string, f field) string {
	return "gen." + schema + f.api
}

// fromStore writes the function that converts a store row to the API type.
// It returns true when the function also returns an error.
func (rc resourceCode) fromStore(g *goFile) bool {
	withErr := false
	for _, f := range rc.fields {
		if f.Type.Name == "json" || (f.Nullable && f.Type.Name == "decimal") {
			withErr = true
		}
	}
	name := rc.conv + "FromStore"
	result, zero := "gen."+rc.item, "gen."+rc.item+"{}"
	g.p("// %s converts a store row to the API type.", name)
	if withErr {
		g.p("func %s(row store.%s) (%s, error) {", name, rc.row, result)
	} else {
		g.p("func %s(row store.%s) %s {", name, rc.row, result)
	}
	g.p("item := %s{", result)
	g.p("Id: row.ID,")
	var blocks []string
	for _, f := range rc.fields {
		src := "row." + f.store
		dst := "item." + f.api
		if !f.Nullable {
			switch f.Type.Name {
			case "date":
				g.p("%s: openapi_types.Date{Time: %s},", f.api, src)
			case "enum":
				g.p("%s: %s(%s),", f.api, enumType(rc.item, f), src)
			case "json":
				blocks = append(blocks, fmt.Sprintf("if err := json.Unmarshal(%s, &%s); err != nil {\nreturn %s, fmt.Errorf(\"decode %s.%s: %%w\", err)\n}", src, dst, zero, rc.spec.Name.Table(), f.col))
			default:
				g.p("%s: %s,", f.api, src)
			}
			continue
		}
		typ, value := pgtypeOf(f.Field)
		switch {
		case f.Type.Name == "enum":
			blocks = append(blocks, fmt.Sprintf("if %s.Valid {\nvalue := %s(%s.String)\n%s = &value\n}", src, enumType(rc.item, f), src, dst))
		case f.Type.Name == "decimal":
			blocks = append(blocks, fmt.Sprintf("if %s.Valid {\nvalue, err := %s.Value()\nif err != nil {\nreturn %s, fmt.Errorf(\"read %s.%s: %%w\", err)\n}\ntext := fmt.Sprint(value)\n%s = &text\n}", src, src, zero, rc.spec.Name.Table(), f.col, dst))
		case typ != "":
			blocks = append(blocks, fmt.Sprintf("if %s.Valid {\n%s = &%s.%s\n}", src, dst, src, value))
		case f.Type.Name == "date":
			blocks = append(blocks, fmt.Sprintf("if %s != nil {\n%s = &openapi_types.Date{Time: *%s}\n}", src, dst, src))
		case f.Type.Name == "json":
			blocks = append(blocks, fmt.Sprintf("if %s != nil {\nif err := json.Unmarshal(%s, &%s); err != nil {\nreturn %s, fmt.Errorf(\"decode %s.%s: %%w\", err)\n}\n}", src, src, dst, zero, rc.spec.Name.Table(), f.col))
		default: // time and uuid: both sides are pointers.
			g.p("%s: %s,", f.api, src)
		}
	}
	g.p("CreatedAt: row.CreatedAt,")
	g.p("UpdatedAt: row.UpdatedAt,")
	g.p("}")
	for _, b := range blocks {
		g.p("%s", b)
	}
	if withErr {
		g.p("return item, nil")
	} else {
		g.p("return item")
	}
	g.p("}")
	g.p("")
	return withErr
}

// toStore writes the function that validates an API input and converts it
// to the store values of the create and update queries.
func (rc resourceCode) toStore(g *goFile) {
	single := len(rc.fields) == 1
	input := "gen." + rc.item + "Input"
	name := rc.conv + "Params"
	result := "store.Create" + rc.item + "Params"
	target := func(f field) string { return "params." + f.store }
	if single {
		result = storeType(rc.fields[0].Field)
		target = func(field) string { return "params" }
	}
	needsErrs := false
	for _, f := range rc.fields {
		switch f.Type.Name {
		case "enum", "json", "decimal":
			needsErrs = true
		}
	}
	g.p("// %s validates an API input and converts it to store values.", name)
	g.p("func %s(in %s) (%s, error) {", name, input, result)
	if needsErrs {
		g.p("var errs []httpx.FieldError")
	}
	var literal, blocks []string
	for _, f := range rc.fields {
		src := "in." + f.api
		dst := target(f)
		if !f.Nullable {
			switch f.Type.Name {
			case "date":
				literal = append(literal, src+".Time")
			case "enum":
				literal = append(literal, "string("+src+")")
				blocks = append(blocks, fmt.Sprintf("if !%s.Valid() {\nerrs = append(errs, httpx.FieldError{Field: %q, Message: %q})\n}", src, f.col, "must be one of: "+strings.Join(f.Type.Enum, ", ")))
			case "decimal":
				literal = append(literal, src)
				blocks = append(blocks, fmt.Sprintf("if err := new(pgtype.Numeric).Scan(%s); err != nil {\nerrs = append(errs, httpx.FieldError{Field: %q, Message: %q})\n}", src, f.col, "must be a decimal number"))
			case "json":
				literal = append(literal, "")
				blocks = append(blocks, fmt.Sprintf("if %s == nil {\nerrs = append(errs, httpx.FieldError{Field: %q, Message: %q})\n} else {\ndata, err := json.Marshal(%s)\nif err != nil {\nreturn %s, err\n}\n%s = data\n}", src, f.col, "must be a JSON object", src, zeroOf(result), dst))
			default:
				literal = append(literal, src)
			}
			continue
		}
		typ, value := pgtypeOf(f.Field)
		switch {
		case f.Type.Name == "enum":
			literal = append(literal, "")
			blocks = append(blocks, fmt.Sprintf("if %s != nil {\nif !%s.Valid() {\nerrs = append(errs, httpx.FieldError{Field: %q, Message: %q})\n}\n%s = pgtype.Text{String: string(*%s), Valid: true}\n}", src, src, f.col, "must be one of: "+strings.Join(f.Type.Enum, ", "), dst, src))
		case f.Type.Name == "decimal":
			literal = append(literal, "")
			blocks = append(blocks, fmt.Sprintf("if %s != nil {\nif err := %s.Scan(*%s); err != nil {\nerrs = append(errs, httpx.FieldError{Field: %q, Message: %q})\n}\n}", src, dst, src, f.col, "must be a decimal number"))
		case typ != "":
			literal = append(literal, "")
			blocks = append(blocks, fmt.Sprintf("if %s != nil {\n%s = pgtype.%s{%s: *%s, Valid: true}\n}", src, dst, typ, value, src))
		case f.Type.Name == "date":
			literal = append(literal, "")
			blocks = append(blocks, fmt.Sprintf("if %s != nil {\n%s = &%s.Time\n}", src, dst, src))
		case f.Type.Name == "json":
			literal = append(literal, "")
			blocks = append(blocks, fmt.Sprintf("if %s != nil {\ndata, err := json.Marshal(*%s)\nif err != nil {\nreturn %s, err\n}\n%s = data\n}", src, src, zeroOf(result), dst))
		default: // time and uuid: both sides are pointers.
			literal = append(literal, src)
		}
	}
	if single {
		if literal[0] != "" {
			g.p("params := %s", literal[0])
		} else {
			g.p("var params %s", result)
		}
	} else {
		g.p("params := %s{", result)
		for i, f := range rc.fields {
			if literal[i] != "" {
				g.p("%s: %s,", f.store, literal[i])
			}
		}
		g.p("}")
	}
	for _, b := range blocks {
		g.p("%s", b)
	}
	if needsErrs {
		g.p("if len(errs) > 0 {")
		g.p("return %s, invalid(errs)", zeroOf(result))
		g.p("}")
	}
	g.p("return params, nil")
	g.p("}")
	g.p("")
}

// zeroOf returns the zero value expression of a Go type.
func zeroOf(typ string) string {
	switch {
	case typ == "string":
		return `""`
	case typ == "bool":
		return "false"
	case strings.HasPrefix(typ, "*"), strings.HasPrefix(typ, "[]"):
		return "nil"
	case strings.HasPrefix(typ, "int"), strings.HasPrefix(typ, "float"):
		return "0"
	}
	return typ + "{}"
}

// handlersGo renders api/internal/handlers/<plural>.go.
func (rc resourceCode) handlersGo() ([]byte, error) {
	n := rc.spec.Name
	singular, plural := n.Pascal(), n.PluralPascal()
	words, pluralWords := strings.ReplaceAll(n.Snake(), "_", " "), strings.ReplaceAll(n.PluralSnake(), "_", " ")
	g := &goFile{pkg: "handlers", local: rc.imports()}
	g.p("func init() {")
	g.p("required := []string{")
	for _, f := range rc.fields {
		if !f.Nullable {
			g.p("%q,", f.col)
		}
	}
	g.p("}")
	g.p("requiredFields[%q] = required", "POST /api/"+n.PluralSnake())
	g.p("requiredFields[%q] = required", "PUT /api/"+n.PluralSnake()+"/{id}")
	g.p("}")
	g.p("")
	withErr := rc.fromStore(g)
	rc.toStore(g)
	single := len(rc.fields) == 1
	convert := func(ok string) {
		if withErr {
			g.p("item, err := %sFromStore(row)", rc.conv)
			g.p("if err != nil {")
			g.p("return nil, err")
			g.p("}")
			g.p("return %s(item), nil", ok)
		} else {
			g.p("return %s(%sFromStore(row)), nil", ok, rc.conv)
		}
	}

	g.p("// List%s returns one page of %s in id order, and the total count.", plural, pluralWords)
	g.p("func (s *Server) List%s(ctx context.Context, req gen.List%sRequestObject) (gen.List%sResponseObject, error) {", plural, plural, plural)
	g.p("limit, offset, err := page(req.Params.Limit, req.Params.Offset)")
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	g.p("rows, err := s.Queries.List%s(ctx, store.List%sParams{RowLimit: limit, RowOffset: offset})", plural, plural)
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	g.p("total, err := s.Queries.Count%s(ctx)", plural)
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	g.p("items := make([]gen.%s, 0, len(rows))", rc.item)
	g.p("for _, row := range rows {")
	if withErr {
		g.p("item, err := %sFromStore(row)", rc.conv)
		g.p("if err != nil {")
		g.p("return nil, err")
		g.p("}")
		g.p("items = append(items, item)")
	} else {
		g.p("items = append(items, %sFromStore(row))", rc.conv)
	}
	g.p("}")
	g.p("return gen.List%s200JSONResponse{Items: items, Total: total}, nil", plural)
	g.p("}")
	g.p("")

	g.p("// Create%s inserts a %s.", singular, words)
	g.p("func (s *Server) Create%s(ctx context.Context, req gen.Create%sRequestObject) (gen.Create%sResponseObject, error) {", singular, singular, singular)
	g.p("params, err := %sParams(*req.Body)", rc.conv)
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	g.p("row, err := s.Queries.Create%s(ctx, params)", singular)
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	convert("gen.Create" + singular + "201JSONResponse")
	g.p("}")
	g.p("")

	g.p("// Get%s returns one %s.", singular, words)
	g.p("func (s *Server) Get%s(ctx context.Context, req gen.Get%sRequestObject) (gen.Get%sResponseObject, error) {", singular, singular, singular)
	g.p("row, err := s.Queries.Get%s(ctx, req.Id)", singular)
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	convert("gen.Get" + singular + "200JSONResponse")
	g.p("}")
	g.p("")

	g.p("// Update%s replaces every writable field of a %s.", singular, words)
	g.p("func (s *Server) Update%s(ctx context.Context, req gen.Update%sRequestObject) (gen.Update%sResponseObject, error) {", singular, singular, singular)
	g.p("params, err := %sParams(*req.Body)", rc.conv)
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	g.p("row, err := s.Queries.Update%s(ctx, store.Update%sParams{", singular, singular)
	g.p("ID: req.Id,")
	for _, f := range rc.fields {
		if single {
			g.p("%s: params,", f.store)
		} else {
			g.p("%s: params.%s,", f.store, f.store)
		}
	}
	g.p("})")
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	convert("gen.Update" + singular + "200JSONResponse")
	g.p("}")
	g.p("")

	g.p("// Delete%s deletes a %s.", singular, words)
	g.p("func (s *Server) Delete%s(ctx context.Context, req gen.Delete%sRequestObject) (gen.Delete%sResponseObject, error) {", singular, singular, singular)
	g.p("deleted, err := s.Queries.Delete%s(ctx, req.Id)", singular)
	g.p("if err != nil {")
	g.p("return nil, err")
	g.p("}")
	g.p("if deleted == 0 {")
	g.p("return nil, errNotFound")
	g.p("}")
	g.p("return gen.Delete%s204Response{}, nil", singular)
	g.p("}")
	return g.bytes()
}

// sample returns a JSON value for f. The second sample differs from the
// first, and it is null for a nullable field. parentVar names the Go
// variable that holds the id of a referenced row.
func sample(f field, second bool, parentVar string) string {
	if second && f.Nullable {
		return "null"
	}
	pick := func(a, b string) string {
		if second {
			return b
		}
		return a
	}
	switch f.Type.Name {
	case "string", "text":
		return pick(fmt.Sprintf("%q", strings.ReplaceAll(f.col, "_", " ")+" 1"), fmt.Sprintf("%q", strings.ReplaceAll(f.col, "_", " ")+" 2"))
	case "int", "bigint":
		return pick("1", "2")
	case "bool":
		return pick("true", "false")
	case "float":
		return pick("1.5", "2.5")
	case "decimal":
		return pick(`"1.5"`, `"2.25"`)
	case "time":
		return pick(`"2026-01-02T03:04:05Z"`, `"2026-02-03T04:05:06Z"`)
	case "date":
		return pick(`"2026-01-02"`, `"2026-02-03"`)
	case "uuid":
		return pick(`"00000000-0000-4000-8000-000000000001"`, `"00000000-0000-4000-8000-000000000002"`)
	case "json":
		return pick(`{"key": "value 1"}`, `{"key": "value 2"}`)
	case "enum":
		return pick(fmt.Sprintf("%q", f.Type.Enum[0]), fmt.Sprintf("%q", f.Type.Enum[len(f.Type.Enum)-1]))
	case "ref":
		if parentVar == "" {
			return "null"
		}
		return "%d"
	}
	return "null"
}

// badSample returns a JSON value of the wrong type for f.
func badSample(f field) string {
	if f.Type.GoAPI == "string" {
		return "42"
	}
	return `"not valid"`
}

// handlersTestGo renders api/internal/handlers/<plural>_test.go.
func (rc resourceCode) handlersTestGo(parents []parentInsert) ([]byte, error) {
	n := rc.spec.Name
	plural := n.PluralPascal()
	base := "/api/" + n.PluralSnake()
	g := &goFile{pkg: "handlers_test", local: rc.imports()}

	parentVars := map[string]string{}
	for _, f := range rc.fields {
		if f.Type.Ref == "" || f.Type.Ref == n.Snake() {
			continue
		}
		parent, _ := fields.ParseName(f.Type.Ref)
		parentVars[parent.Table()] = parent.Camel() + "ID"
	}
	body := func(second bool) (string, []string) {
		var parts, args []string
		for _, f := range rc.fields {
			var parentVar string
			if f.Type.Ref != "" {
				parent, _ := fields.ParseName(f.Type.Ref)
				parentVar = parentVars[parent.Table()]
			}
			v := sample(f, second, parentVar)
			if v == "%d" {
				args = append(args, parentVar)
			}
			parts = append(parts, fmt.Sprintf("%q: %s", f.col, v))
		}
		return "{" + strings.Join(parts, ", ") + "}", args
	}
	bodyExpr := func(second bool) string {
		text, args := body(second)
		if len(args) == 0 {
			return "strings.NewReader(`" + text + "`)"
		}
		return "strings.NewReader(fmt.Sprintf(`" + text + "`, " + strings.Join(args, ", ") + "))"
	}
	// changed is the first field whose two samples differ. Ref fields keep
	// the same row.
	var changed *field
	for i, f := range rc.fields {
		if f.Type.Ref == "" && sample(f, false, "") != sample(f, true, "") {
			changed = &rc.fields[i]
			break
		}
	}

	g.p("func Test%sCRUD(t *testing.T) {", plural)
	g.p("t.Parallel()")
	g.p("pool := testkit.DBWithMigrations(t, migrations.FS)")
	g.p("handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {")
	g.p("routes.Register(mux, routes.Deps{DB: pool})")
	g.p("})).Handler()")
	if len(parents) > 0 {
		g.p("")
		g.p("// Rows that the %s refer to.", strings.ReplaceAll(n.PluralSnake(), "_", " "))
	}
	for _, p := range parents {
		if v, ok := parentVars[p.Table]; ok {
			g.p("var %s int64", v)
			g.p("if err := pool.QueryRow(t.Context(), `%s RETURNING id`).Scan(&%s); err != nil {", p.SQL, v)
		} else {
			g.p("if _, err := pool.Exec(t.Context(), `%s`); err != nil {", p.SQL)
		}
		g.p("t.Fatalf(\"insert a row into %s: %%v\", err)", p.Table)
		g.p("}")
	}
	g.p("")
	g.p("rec := testkit.Do(t, handler, http.MethodPost, %q, %s)", base, bodyExpr(false))
	g.p("if rec.Code != http.StatusCreated {")
	g.p("t.Fatalf(\"create: status %%d, want %%d\\n%%s\", rec.Code, http.StatusCreated, rec.Body)")
	g.p("}")
	g.p("created := testkit.DecodeJSON[gen.%s](t, rec)", rc.item)
	g.p("path := fmt.Sprintf(\"%s/%%d\", created.Id)", base)
	g.p("")
	g.p("rec = testkit.Do(t, handler, http.MethodGet, path, nil)")
	g.p("if rec.Code != http.StatusOK {")
	g.p("t.Fatalf(\"get: status %%d, want %%d\\n%%s\", rec.Code, http.StatusOK, rec.Body)")
	g.p("}")
	g.p("if got := testkit.DecodeJSON[gen.%s](t, rec); !reflect.DeepEqual(got, created) {", rc.item)
	g.p("t.Fatalf(\"get: got %%+v, want %%+v\", got, created)")
	g.p("}")
	g.p("")
	g.p("rec = testkit.Do(t, handler, http.MethodGet, %q, nil)", base+"?limit=10")
	g.p("if rec.Code != http.StatusOK {")
	g.p("t.Fatalf(\"list: status %%d, want %%d\\n%%s\", rec.Code, http.StatusOK, rec.Body)")
	g.p("}")
	g.p("list := testkit.DecodeJSON[gen.%sList](t, rec)", rc.item)
	g.p("if list.Total != 1 || len(list.Items) != 1 || !reflect.DeepEqual(list.Items[0], created) {")
	g.p("t.Fatalf(\"list: got %%+v, want one item %%+v\", list, created)")
	g.p("}")
	g.p("")
	g.p("rec = testkit.Do(t, handler, http.MethodPut, path, %s)", bodyExpr(true))
	g.p("if rec.Code != http.StatusOK {")
	g.p("t.Fatalf(\"update: status %%d, want %%d\\n%%s\", rec.Code, http.StatusOK, rec.Body)")
	g.p("}")
	g.p("updated := testkit.DecodeJSON[gen.%s](t, rec)", rc.item)
	if changed != nil {
		g.p("if updated.Id != created.Id || reflect.DeepEqual(updated.%s, created.%s) {", changed.api, changed.api)
		g.p("t.Fatalf(\"update: got %%+v, want a new %s\", updated)", changed.col)
	} else {
		g.p("if updated.Id != created.Id {")
		g.p("t.Fatalf(\"update: got id %%d, want %%d\", updated.Id, created.Id)")
	}
	g.p("}")
	g.p("rec = testkit.Do(t, handler, http.MethodGet, path, nil)")
	g.p("if got := testkit.DecodeJSON[gen.%s](t, rec); !reflect.DeepEqual(got, updated) {", rc.item)
	g.p("t.Fatalf(\"get after update: got %%+v, want %%+v\", got, updated)")
	g.p("}")
	g.p("")
	g.p("rec = testkit.Do(t, handler, http.MethodDelete, path, nil)")
	g.p("if rec.Code != http.StatusNoContent {")
	g.p("t.Fatalf(\"delete: status %%d, want %%d\\n%%s\", rec.Code, http.StatusNoContent, rec.Body)")
	g.p("}")
	g.p("rec = testkit.Do(t, handler, http.MethodGet, path, nil)")
	g.p("if rec.Code != http.StatusNotFound {")
	g.p("t.Fatalf(\"get after delete: status %%d, want %%d\\n%%s\", rec.Code, http.StatusNotFound, rec.Body)")
	g.p("}")
	g.p("}")
	g.p("")

	first := rc.fields[0]
	g.p("func Test%sRejectsBadInput(t *testing.T) {", plural)
	g.p("t.Parallel()")
	g.p("// These requests fail before a query runs, so they need no database.")
	g.p("handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {")
	g.p("routes.Register(mux, routes.Deps{})")
	g.p("})).Handler()")
	g.p("tests := []struct {")
	g.p("name, method, path, body string")
	g.p("status int")
	g.p("}{")
	g.p("{\"wrong type\", http.MethodPost, %q, `{%q: %s}`, http.StatusUnprocessableEntity},", base, first.col, badSample(first))
	g.p("{\"not JSON\", http.MethodPost, %q, `{`, http.StatusBadRequest},", base)
	g.p("{\"bad id\", http.MethodGet, %q, \"\", http.StatusBadRequest},", base+"/abc")
	g.p("{\"limit too large\", http.MethodGet, %q, \"\", http.StatusUnprocessableEntity},", base+"?limit=201")
	g.p("}")
	g.p("for _, tc := range tests {")
	g.p("t.Run(tc.name, func(t *testing.T) {")
	g.p("t.Parallel()")
	g.p("var body io.Reader")
	g.p("if tc.body != \"\" {")
	g.p("body = strings.NewReader(tc.body)")
	g.p("}")
	g.p("rec := testkit.Do(t, handler, tc.method, tc.path, body)")
	g.p("if rec.Code != tc.status {")
	g.p("t.Fatalf(\"status %%d, want %%d\\n%%s\", rec.Code, tc.status, rec.Body)")
	g.p("}")
	g.p("if got := rec.Header().Get(\"Content-Type\"); got != \"application/problem+json\" {")
	g.p("t.Fatalf(\"Content-Type %%q, want application/problem+json\", got)")
	g.p("}")
	g.p("})")
	g.p("}")
	g.p("}")
	rc.requiredFieldsTest(g, base)
	return g.bytes()
}

// requiredFieldsTest tests presence through the complete generated HTTP stack.
func (rc resourceCode) requiredFieldsTest(g *goFile, base string) {
	var required []string
	for _, f := range rc.fields {
		if !f.Nullable {
			required = append(required, f.col)
		}
	}
	if len(required) == 0 {
		return
	}
	sort.Strings(required)
	// Each individual case supplies valid values for every other field.
	body := func(changed string, null bool) string {
		var parts []string
		for _, f := range rc.fields {
			value := sample(f, false, "")
			if f.Type.Ref != "" && !f.Nullable {
				value = "1"
			}
			if f.col == changed {
				if !null {
					continue
				}
				value = "null"
			}
			parts = append(parts, fmt.Sprintf("%q: %s", f.col, value))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	g.p("")
	g.p("func Test%sRequiresFields(t *testing.T) {", rc.spec.Name.PluralPascal())
	g.p("t.Parallel()")
	g.p("handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {")
	g.p("routes.Register(mux, routes.Deps{})")
	g.p("})).Handler()")
	g.p("tests := []struct {")
	g.p("name, body string")
	g.p("want []httpx.FieldError")
	g.p("}{")
	emit := func(name, body string, cols []string) {
		g.p("{%q, %q, []httpx.FieldError{", name, body)
		for _, col := range cols {
			g.p("{Field: %q, Message: \"is required\"},", col)
		}
		g.p("}},")
	}
	emit("missing all", "{}", required)
	emit("body null", "null", required)
	for _, col := range required {
		emit("missing "+col, body(col, false), []string{col})
		emit("null "+col, body(col, true), []string{col})
	}
	g.p("}")
	g.p("for _, method := range []string{http.MethodPost, http.MethodPut} {")
	g.p("for _, tc := range tests {")
	g.p("t.Run(method+tc.name, func(t *testing.T) {")
	g.p("t.Parallel()")
	g.p("path := %q", base)
	g.p("if method == http.MethodPut { path += \"/1\" }")
	g.p("rec := testkit.Do(t, handler, method, path, strings.NewReader(tc.body))")
	g.p("if rec.Code != http.StatusUnprocessableEntity {")
	g.p("t.Fatalf(\"status %%d, want 422: %%s\", rec.Code, rec.Body)")
	g.p("}")
	g.p("if got := rec.Header().Get(\"Content-Type\"); got != \"application/problem+json\" {")
	g.p("t.Fatalf(\"Content-Type %%q, want application/problem+json\", got)")
	g.p("}")
	g.p("problem := testkit.DecodeJSON[httpx.Problem](t, rec)")
	g.p("if !reflect.DeepEqual(problem.Errors, tc.want) {")
	g.p("t.Fatalf(\"errors %%+v, want %%+v\", problem.Errors, tc.want)")
	g.p("}")
	g.p("})")
	g.p("}")
	g.p("}")
	g.p("}")
}
