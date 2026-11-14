package live

import (
	"fmt"
	"go/format"
	"strings"

	"github.com/stopyellingatme/spinup/internal/project"
)

// FeedSource renders the snapshot loader, the stream, and their access guard.
func FeedSource(s FeedSpec) ([]byte, error) {
	var src strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&src, format+"\n", args...) }
	name, plural, singular := s.Name.PluralSnake(), s.Name.PluralPascal(), s.Name.Pascal()
	constant := s.Name.PluralCamel() + "Feed"
	p("package live\n\nimport (\n\"context\"\n\"fmt\"\n\"net/http\"\n\n\"github.com/stopyellingatme/spinup/kit/auth\"\nkitlive \"github.com/stopyellingatme/spinup/kit/x/live\"\n%q\n%q", s.Module+"/internal/gen", s.Module+"/internal/handlers")
	if len(s.Roles) > 0 {
		p("%q", s.Module+"/internal/roles")
	}
	p(")\n\nconst %s = %q", constant, name)
	p("// spinup:live-feed %s register%sFeed\nfunc register%sFeed(mux *http.ServeMux, h *kitlive.Hub, d Deps) {", name, plural, plural)
	p("key := func(r *http.Request) (kitlive.Key, error) {")
	user := "_"
	if s.Owned || len(s.Roles) > 0 {
		user = "user"
	}
	p("%s, ok := auth.UserFrom(r.Context())\nif !ok { return \"\", kitlive.ErrUnauthorized }", user)
	if len(s.Roles) > 0 {
		roles := make([]string, len(s.Roles))
		for i, role := range s.Roles {
			roles[i] = "roles." + project.RoleConst(role)
		}
		p("if !user.HasRole(%s) {return \"\",kitlive.ErrForbidden}", strings.Join(roles, ", "))
	}
	if s.Owned {
		p("return kitlive.OwnedKey(%s, user.ID), nil", constant)
	} else {
		p("return kitlive.SharedKey(%s), nil", constant)
	}
	p("}\nserver := handlers.NewServer(d.DB)")
	p("mux.Handle(%q, kitlive.Snapshot(h, key, func(ctx context.Context, _ kitlive.Key, limit int) ([]gen.%s, error) {", "GET /api/live/"+name, singular)
	p("limit32 := int32(min(limit, 200))")
	p("response, err := server.List%s(ctx, gen.List%sRequestObject{Params: gen.List%sParams{Limit: &limit32}})", plural, plural, plural)
	p("if err != nil {return nil,err}\nswitch result := response.(type) {\ncase gen.List%s200JSONResponse:\nreturn result.Items,nil\ndefault:\nreturn nil,fmt.Errorf(\"unexpected List%s response %%T\",response)\n}\n}))", plural, plural)
	p("mux.Handle(%q, h.Stream(key))\n}", "GET /api/live/"+name+"/live")
	data, err := format.Source([]byte(src.String()))
	if err != nil {
		return nil, fmt.Errorf("cannot format live feed: %w\nTry: report this generator bug", err)
	}
	return data, nil
}
