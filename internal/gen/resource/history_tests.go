package resource

import "strings"

func (rc resourceCode) auditTestRole() string {
	if len(rc.spec.Roles) > 0 {
		return rc.spec.Roles[0]
	}
	if len(rc.spec.Audit.HistoryRoles) > 0 {
		return rc.spec.Audit.HistoryRoles[0]
	}
	return ""
}

func (rc resourceCode) historyTestRole() string {
	if len(rc.spec.Audit.HistoryRoles) > 0 {
		return rc.spec.Audit.HistoryRoles[0]
	}
	return rc.auditTestRole()
}

func (rc resourceCode) auditSnapshot(g *goFile, name string) {
	g.p("var %sJSON []byte", name)
	g.p("if err:=pool.QueryRow(t.Context(),%q,created.Id).Scan(&%sJSON); err!=nil {t.Fatal(err)}", "SELECT to_jsonb(t) FROM "+rc.spec.Name.Table()+" t WHERE id=$1", name)
	g.p("var %s map[string]any", name)
	g.p("if err:=json.Unmarshal(%sJSON,&%s); err!=nil {t.Fatal(err)}", name, name)
}

func (rc resourceCode) auditCRUDAssertions(g *goFile, updated bool) {
	g.p("history := authtest.AsRole(historyBase,userA,%q)", rc.historyTestRole())
	g.p("rec = testkit.Do(t,history,http.MethodGet,path+\"/history\",nil)")
	g.p("if rec.Code!=http.StatusOK {t.Fatalf(\"history status %%d: %%s\",rec.Code,rec.Body)}")
	g.p("if rec.Header().Get(\"Cache-Control\")!=\"no-store\" {t.Fatal(\"history must not be cached\")}")
	g.p("events := testkit.DecodeJSON[struct{Items []audit.Event `json:\"items\"`}](t,rec).Items")
	actions := []string{"delete", "create"}
	if updated {
		actions = []string{"delete", "update", "create"}
	}
	g.p("actions := []string{\"%s\"}", strings.Join(actions, "\", \""))
	g.p("if len(events)!=len(actions) {t.Fatalf(\"events = %%+v, want actions %%v\",events,actions)}")
	g.p("for i,event:= range events {")
	g.p("if event.Table!=%q || event.RecordID!=strconv.FormatInt(created.Id,10) {t.Fatalf(\"event subject = %%q %%q, want %%q %%d\",event.Table,event.RecordID,%q,created.Id)}", rc.spec.Name.Table(), rc.spec.Name.Table())
	g.p("if event.Action!=actions[i] || event.Actor.Kind!=audit.KindUser || event.Actor.ID!=userA || event.Actor.Email!=\"owner-a@example.com\" || event.Actor.Role!=%q || event.Actor.Source!=audit.SourceAPI {t.Fatalf(\"event = %%+v\",event)}", rc.auditTestRole())
	g.p("var changes map[string]map[string]any")
	g.p("if err:=json.Unmarshal(event.Changes,&changes); err!=nil {t.Fatal(err)}")
	var cols []string
	for _, f := range rc.fields {
		cols = append(cols, "\""+f.col+"\"")
	}
	g.p("for _,field :=range []string{%s} {", strings.Join(cols, ", "))
	g.p("oldValue,newValue:=before[field],after[field]")
	g.p("change,present:=changes[field]")
	g.p("if event.Action==\"create\" && oldValue==nil || event.Action==\"update\" && reflect.DeepEqual(oldValue,newValue) {if present {t.Fatalf(\"unexpected change %%s: %%v\",field,change)}; continue}")
	g.p("if !present {t.Fatalf(\"missing change %%s: %%s\",field,event.Changes)}")
	if len(rc.spec.Audit.Redact) > 0 {
		var redact []string
		for _, col := range rc.spec.Audit.Redact {
			redact = append(redact, "\""+col+"\"")
		}
		g.p("redacted := false")
		g.p("for _,col:=range []string{%s} {if field==col {redacted=true}}", strings.Join(redact, ", "))
		g.p("if redacted {if change[\"redacted\"]!=true || len(change)!=1 {t.Fatalf(\"redacted value leaked: %%v\",change)}; continue}")
	}
	g.p("want:=map[string]any{}")
	g.p("switch event.Action {case \"create\":want[\"new\"]=oldValue; case \"update\":want[\"old\"]=oldValue;want[\"new\"]=newValue; case \"delete\":want[\"old\"]=newValue}")
	g.p("if !reflect.DeepEqual(change,want) {t.Fatalf(\"%%s %%s: got %%v, want %%v\",event.Action,field,change,want)}")
	g.p("}")
	g.p("}")
	if rc.spec.Owned {
		g.p("otherHistory:=authtest.AsRole(historyBase,userB,%q)", rc.historyTestRole())
		g.p("rec=testkit.Do(t,otherHistory,http.MethodGet,path+\"/history\",nil)")
		g.p("if rec.Code!=http.StatusOK {t.Fatalf(\"other history status %%d: %%s\",rec.Code,rec.Body)}")
		g.p("otherEvents:=testkit.DecodeJSON[struct{Items []audit.Event `json:\"items\"`}](t,rec)")
		g.p("if len(otherEvents.Items)!=0 {t.Fatalf(\"another owner saw events: %%+v\",otherEvents)}")
	}
}

func (rc resourceCode) historyGuardTests(g *goFile, base string) {
	g.p("func Test%sHistoryGuards(t *testing.T) {", rc.spec.Name.PluralPascal())
	g.p("t.Parallel()")
	g.p("base:=app.New(app.WithRoutes(func(mux *http.ServeMux) {routes.Register(mux,routes.Deps{})})).Handler()")
	g.p("handler:=authtest.AsRole(base,1,%q)", rc.historyTestRole())
	g.p("for _,query:=range []string{\"limit=201\",\"offset=10001\",\"limit=0\",\"offset=-1\",\"before_id=0\",\"before_id=-1\"} {")
	g.p("rec:=testkit.Do(t,handler,http.MethodGet,%q+query,nil)", base+"/1/history?")
	g.p("if rec.Code!=http.StatusUnprocessableEntity || rec.Header().Get(\"Content-Type\")!=\"application/problem+json\" || !strings.Contains(rec.Body.String(),\"Try:\") {t.Fatalf(\"%%s: %%d %%s\",query,rec.Code,rec.Body)}")
	g.p("problem:=testkit.DecodeJSON[httpx.Problem](t,rec)")
	g.p("if problem.Type!=\"about:blank\" || problem.Title!=http.StatusText(http.StatusUnprocessableEntity) || problem.Status!=http.StatusUnprocessableEntity || rec.Header().Get(\"Cache-Control\")!=\"no-store\" {t.Fatalf(\"invalid history problem: %%+v\",problem)}")
	g.p("}")
	if len(rc.spec.Audit.HistoryRoles) > 0 {
		g.p("denied:=authtest.AsRole(base,1,%q)", "__spinup_forbidden_role")
		g.p("rec:=testkit.Do(t,denied,http.MethodGet,%q,nil)", base+"/1/history")
		g.p("if rec.Code!=http.StatusForbidden {t.Fatalf(\"history status %%d, want 403: %%s\",rec.Code,rec.Body)}")
	}
	g.p("unauthorized:=testkit.Do(t,base,http.MethodGet,%q,nil)", base+"/1/history")
	g.p("if unauthorized.Code!=http.StatusUnauthorized {t.Fatalf(\"history status %%d, want 401: %%s\",unauthorized.Code,unauthorized.Body)}")
	g.p("}")
}
