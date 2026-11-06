package resource

import "strings"

// bodyTextFunc returns a Go string expression with a JSON body. vars name
// the referenced rows. When col is set, its value is the Go expression value.
type bodyTextFunc func(second bool, vars map[string]string, col, value string) string

// ownerRefTest writes the checks that user B cannot refer to rows of user A
// through an owner reference. The code runs inside the CRUD test, after the
// rows of both users exist.
func (rc resourceCode) ownerRefTest(g *goFile, base string, bodyText bodyTextFunc, parentVars, otherVars map[string]string) {
	g.p("")
	g.p("// User B cannot refer to rows of user A. Such a row gets the same answer")
	g.p("// as a row that does not exist, so the answer does not show that it exists.")
	g.p("rec = testkit.Do(t, other, http.MethodPost, %q, strings.NewReader(%s))", base, bodyText(false, otherVars, "", ""))
	g.p("if rec.Code != http.StatusCreated { t.Fatalf(\"other user create: status %%d: %%s\", rec.Code, rec.Body) }")
	g.p("otherPath := fmt.Sprintf(\"%s/%%d\", testkit.DecodeJSON[gen.%s](t, rec).Id)", base, rc.item)
	g.p("for _, tc := range []struct{ name, method, path, body, field string }{")
	for _, r := range rc.spec.ownerRefs {
		mine := "created.Id"
		if !r.Self {
			mine = parentVars[r.Table]
		}
		g.p("{%q, http.MethodPost, %q, %s, %q},", "create with "+r.Column+" of user A", base, bodyText(false, otherVars, r.Column, mine), r.Column)
		g.p("{%q, http.MethodPost, %q, %s, %q},", "create with a missing "+r.Column, base, bodyText(false, otherVars, r.Column, "int64(-1)"), r.Column)
		g.p("{%q, http.MethodPut, otherPath, %s, %q},", "update with "+r.Column+" of user A", bodyText(true, otherVars, r.Column, mine), r.Column)
	}
	g.p("} {")
	g.p("rec := testkit.Do(t, other, tc.method, tc.path, strings.NewReader(tc.body))")
	g.p("if rec.Code != http.StatusUnprocessableEntity { t.Fatalf(\"%%s: status %%d, want 422: %%s\", tc.name, rec.Code, rec.Body) }")
	g.p("problem := testkit.DecodeJSON[httpx.Problem](t, rec)")
	g.p("if want := []httpx.FieldError{{Field: tc.field, Message: %q}}; !reflect.DeepEqual(problem.Errors, want) {", refMessage)
	g.p("t.Fatalf(\"%%s: errors %%+v, want %%+v\", tc.name, problem.Errors, want)")
	g.p("}")
	g.p("}")
}

// ownerDeleteTest writes the check that user A can still delete the rows
// that the owner references point to. A row of user B that referred to one
// of them would block the delete. The rows go in the reverse order of their
// inserts, because a later row can refer to an earlier one.
func (rc resourceCode) ownerDeleteTest(g *goFile, parents []parentInsert, parentVars map[string]string) {
	targets := map[string]bool{}
	for _, r := range rc.spec.ownerRefs {
		if !r.Self {
			targets[r.Table] = true
		}
	}
	if len(targets) == 0 {
		return
	}
	g.p("")
	g.p("// User A can still delete the rows that the %s referred to.", strings.ReplaceAll(rc.spec.Name.PluralSnake(), "_", " "))
	for i := len(parents) - 1; i >= 0; i-- {
		p := parents[i]
		v, ok := parentVars[p.Table]
		if !ok || !targets[p.Table] {
			continue
		}
		g.p("if tag, err := pool.Exec(t.Context(), `DELETE FROM %s WHERE id = $1`, %s); err != nil || tag.RowsAffected() != 1 {", p.Table, v)
		g.p("t.Fatalf(\"delete the %s row of user A: %%v\", err)", p.Table)
		g.p("}")
	}
}

// responseErrorsTest writes a test of ResponseError in server.go. Only the
// resource that writes server.go gets it, so the test matches that file.
func (rc resourceCode) responseErrorsTest(g *goFile, base string) {
	g.p("")
	g.p("// Test%sResponseErrors checks the answers of ResponseError in server.go to", rc.spec.Name.PluralPascal())
	g.p("// errors that are not problems.")
	g.p("func Test%sResponseErrors(t *testing.T) {", rc.spec.Name.PluralPascal())
	g.p("t.Parallel()")
	g.p("// A client that went away gets nothing: nobody reads the answer.")
	g.p("canceled, cancel := context.WithCancel(t.Context())")
	g.p("cancel()")
	g.p("rec := httptest.NewRecorder()")
	g.p("handlers.ResponseError(rec, httptest.NewRequestWithContext(canceled, http.MethodGet, %q, nil), fmt.Errorf(\"list: %%w\", context.Canceled))", base)
	g.p("if rec.Body.Len() != 0 || rec.Header().Get(\"Content-Type\") != \"\" {")
	g.p("t.Fatalf(\"canceled: status %%d, body %%q, want no answer\", rec.Code, rec.Body)")
	g.p("}")
	g.p("")
	g.p("// An expired request deadline is a 503 problem, not a 500.")
	g.p("expired, stop := context.WithDeadline(t.Context(), time.Unix(0, 0))")
	g.p("defer stop()")
	g.p("rec = httptest.NewRecorder()")
	g.p("handlers.ResponseError(rec, httptest.NewRequestWithContext(expired, http.MethodGet, %q, nil), fmt.Errorf(\"list: %%w\", context.DeadlineExceeded))", base)
	g.p("if problem := testkit.DecodeJSON[httpx.Problem](t, rec); rec.Code != http.StatusServiceUnavailable || problem.Status != http.StatusServiceUnavailable {")
	g.p("t.Fatalf(\"deadline: status %%d, problem %%+v, want 503\", rec.Code, problem)")
	g.p("}")
	g.p("")
	g.p("// Any other error is a 500 problem.")
	g.p("rec = httptest.NewRecorder()")
	g.p("handlers.ResponseError(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, %q, nil), errors.New(\"broken\"))", base)
	g.p("if problem := testkit.DecodeJSON[httpx.Problem](t, rec); rec.Code != http.StatusInternalServerError || problem.Detail != \"\" {")
	g.p("t.Fatalf(\"other error: status %%d, problem %%+v, want a 500 without details\", rec.Code, problem)")
	g.p("}")
	g.p("}")
}
