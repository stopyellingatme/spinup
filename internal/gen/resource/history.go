package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

const auditSchemasPath = "api/openapi/schemas/audit_event.yaml"

func historyPathsPath(spec Spec) string {
	return "api/openapi/paths/" + spec.Name.PluralSnake() + "_history.yaml"
}
func historyHandlerPath(spec Spec) string {
	return "api/internal/handlers/" + spec.Name.PluralSnake() + "_history.go"
}

// PlanHistory plans only new history files. It preserves existing resource files.
func PlanHistory(spec Spec, pages bool) (vfs.Plan, []gen.PostStep, error) {
	module, err := apiModule(spec.Root)
	if err != nil {
		return vfs.Plan{}, nil, err
	}
	rc := newResourceCode(spec, module, "")
	handler, err := rc.historyGo()
	if err != nil {
		return vfs.Plan{}, nil, err
	}
	files := []vfs.File{
		{Path: historyPathsPath(spec), Data: historyPathsYAML(spec), Class: string(manifest.Scaffold)},
		{Path: historyHandlerPath(spec), Data: handler, Class: string(manifest.Scaffold)},
	}
	if _, err := os.Stat(filepath.Join(spec.Root, auditSchemasPath)); errors.Is(err, os.ErrNotExist) {
		files = append(files, vfs.File{Path: auditSchemasPath, Data: []byte(auditSchemas), Class: string(manifest.Scaffold)})
	} else if err != nil {
		return vfs.Plan{}, nil, fmt.Errorf("check audit schemas: %w\nTry: check the file permissions", err)
	}
	var plan vfs.Plan
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(spec.Root, f.Path)); err == nil {
			return vfs.Plan{}, nil, fmt.Errorf("cannot add history because %s already exists\nTry: review the existing history files", f.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return vfs.Plan{}, nil, fmt.Errorf("check %s: %w\nTry: check the file permissions", f.Path, err)
		}
		if err := plan.Add(f); err != nil {
			return vfs.Plan{}, nil, err
		}
	}
	steps := []gen.PostStep{gen.PostOpenAPI, gen.PostGofmt}
	if pages {
		p, err := planHistoryPages(spec)
		if err != nil {
			return vfs.Plan{}, nil, err
		}
		plan, err = plan.Merge(p)
		if err != nil {
			return vfs.Plan{}, nil, err
		}
		if len(p.Files) > 0 {
			steps = append(steps, gen.PostClient)
		}
	}
	return plan, steps, nil
}

func historyPathsYAML(spec Spec) []byte {
	var w yamlWriter
	n := spec.Name
	w.line(0, "/api/%s/{id}/history:", n.PluralSnake())
	w.line(1, "get:")
	w.line(2, "operationId: list%sHistory", n.Pascal())
	w.line(2, "summary: Read the history of a %s", words(n.Snake()))
	w.line(2, "tags: [%s]", n.PluralSnake())
	w.line(2, "parameters:")
	w.line(3, "- name: id")
	w.line(4, "in: path")
	w.line(4, "required: true")
	w.line(4, "schema:")
	w.line(5, "type: integer")
	w.line(5, "format: int64")
	for _, p := range []struct {
		name          string
		min, max, def int
	}{{"limit", 1, 200, 50}, {"offset", 0, 10000, 0}, {"before_id", 1, 0, 0}} {
		w.line(3, "- name: %s", p.name)
		w.line(4, "in: query")
		w.line(4, "schema:")
		w.line(5, "type: integer")
		if p.name == "before_id" {
			w.line(5, "format: int64")
		} else {
			w.line(5, "format: int32")
		}
		w.line(5, "minimum: %d", p.min)
		if p.max > 0 {
			w.line(5, "maximum: %d", p.max)
			w.line(5, "default: %d", p.def)
		}
	}
	w.line(2, "responses:")
	w.response("200", "The events, newest first.", "application/json", "AuditEventList")
	// Guards are also checked inside the strict method.
	w.problems(words(n.Snake()), "400", "401", "403", "422")
	return []byte(w.b.String())
}

func (rc resourceCode) historyGo() ([]byte, error) {
	g := &goFile{pkg: "handlers", local: rc.imports()}
	op := "List" + rc.spec.Name.Pascal() + "History"
	response := rc.spec.Name.Camel() + "HistoryResponse"
	g.p("// %s returns the events visible to the signed-in user.", op)
	g.p("func (s *Server) %s(ctx context.Context, req gen.%sRequestObject) (gen.%sResponseObject,error) {", op, op, op)
	if rc.spec.Owned || len(rc.spec.Audit.HistoryRoles) > 0 {
		g.p("user, ok := auth.UserFrom(ctx)")
	} else {
		g.p("_, ok := auth.UserFrom(ctx)")
	}
	g.p("if !ok {return %s{status:http.StatusUnauthorized, body:httpx.Problem{Type:\"about:blank\",Title:http.StatusText(http.StatusUnauthorized),Status:http.StatusUnauthorized, Detail: \"A signed-in user is required. Try: sign in.\"}},nil}", response)
	if len(rc.spec.Audit.HistoryRoles) > 0 {
		var roles []string
		for _, role := range rc.spec.Audit.HistoryRoles {
			roles = append(roles, "roles."+project.RoleConst(role))
		}
		g.p("if !user.HasRole(%s) {return %s{status:http.StatusForbidden, body:httpx.Problem{Type:\"about:blank\",Title:http.StatusText(http.StatusForbidden),Status:http.StatusForbidden, Detail: \"Your role cannot read history. Try: sign in with an allowed role.\"}},nil}", strings.Join(roles, ", "), response)
	}
	g.p("filter := audit.Filter{Table:%q, RecordID:strconv.FormatInt(req.Id,10), Limit:audit.DefaultLimit}", rc.spec.Name.Table())
	if rc.spec.Owned {
		g.p("filter.OwnerID = user.ID")
	}
	g.p("if req.Params.Limit != nil { filter.Limit = int(*req.Params.Limit) }")
	g.p("if req.Params.Offset != nil { filter.Offset = int(*req.Params.Offset) }")
	g.p("if req.Params.BeforeId != nil {filter.BeforeID = *req.Params.BeforeId}")
	g.p("err := filter.Validate()")
	g.p("if err == nil && (filter.Limit < 1 || (req.Params.BeforeId != nil && filter.BeforeID < 1)) {err = fmt.Errorf(\"invalid history page. Try: use a positive limit and cursor.\")}")
	g.p("if err != nil {return %s{status:http.StatusUnprocessableEntity,body:httpx.Problem{Type:\"about:blank\",Title:http.StatusText(http.StatusUnprocessableEntity),Status:http.StatusUnprocessableEntity,Detail:err.Error()}},nil}", response)
	g.p("events,err := audit.List(ctx,s.DB,filter)")
	g.p("if err != nil {")
	g.p("slog.ErrorContext(ctx,\"read resource history\",\"error\",err)")
	g.p("return %s{status:http.StatusInternalServerError,body:httpx.Problem{Type:\"about:blank\",Title:http.StatusText(http.StatusInternalServerError),Status:http.StatusInternalServerError,Detail:\"Cannot read history. Try: check the database schema and retry.\"}},nil", response)
	g.p("}")
	g.p("if events == nil {events = []audit.Event{}}")
	// A JSON round trip keeps the public schema separate from the kit structs.
	g.p("data,err := json.Marshal(events)")
	g.p("if err != nil {return nil,err}")
	g.p("items:=make([]gen.AuditEvent,0,len(events))")
	g.p("if err:=json.Unmarshal(data,&items); err!=nil {return nil,err}")
	g.p("return %s{status:http.StatusOK,body:gen.AuditEventList{Items:items}},nil", response)
	g.p("}")
	g.p("")
	g.p("// %s adds the cache policy to every history response.", response)
	g.p("type %s struct {status int; body any}", response)
	g.p("// Visit%sResponse writes a history result or a problem.", op)
	g.p("func (r %s) Visit%sResponse(w http.ResponseWriter) error {", response, op)
	g.p("w.Header().Set(\"Cache-Control\",\"no-store\")")
	g.p("media := \"application/json\"")
	g.p("if r.status != http.StatusOK {media = \"application/problem+json\"}")
	g.p("w.Header().Set(\"Content-Type\",media)")
	g.p("w.WriteHeader(r.status)")
	g.p("return json.NewEncoder(w).Encode(r.body)")
	g.p("}")
	return g.bytes()
}

const auditSchemas = `AuditEvent:
  type: object
  required: [id, occurred_at, action, table_name, record_id, changes, actor]
  properties:
    id:
      type: integer
      format: int64
    occurred_at:
      type: string
      format: date-time
    action:
      type: string
    table_name:
      type: string
    record_id:
      type: string
    owner_id:
      type: integer
      format: int64
    changes:
      description: Field changes. A row change maps each field to an object with old, new, or redacted. A custom event (audit.Record) may hold any JSON value.
      type: object
      additionalProperties: true
    actor:
      type: object
      required: [kind]
      properties:
        kind:
          type: string
        id:
          type: integer
          format: int64
        email:
          type: string
        role:
          type: string
        source:
          type: string
        reason:
          type: string
        request_id:
          type: string
AuditEventList:
  type: object
  required: [items]
  properties:
    items:
      type: array
      items:
        $ref: "#/components/schemas/AuditEvent"
`
