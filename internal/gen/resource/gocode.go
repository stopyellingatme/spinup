package resource

import (
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strings"
)

// goFile builds the source of one Go file. It adds the imports that the body
// uses and formats the result.
type goFile struct {
	pkg string
	// local maps an import path of the application to its package name.
	local []string
	b     strings.Builder
}

func (g *goFile) p(format string, args ...any) {
	fmt.Fprintf(&g.b, format, args...)
	g.b.WriteByte('\n')
}

// knownImports maps a package name to its import path and an optional name.
var knownImports = []struct{ name, path, alias string }{
	{"context", "context", ""},
	{"errors", "errors", ""},
	{"fmt", "fmt", ""},
	{"http", "net/http", ""},
	{"io", "io", ""},
	{"json", "encoding/json", ""},
	{"reflect", "reflect", ""},
	{"slog", "log/slog", ""},
	{"strings", "strings", ""},
	{"testing", "testing", ""},
	{"time", "time", ""},
	{"app", "github.com/stopyellingatme/spinup/kit/app", ""},
	{"httpx", "github.com/stopyellingatme/spinup/kit/httpx", ""},
	{"openapi_types", "github.com/oapi-codegen/runtime/types", "openapi_types"},
	{"pgconn", "github.com/jackc/pgx/v5/pgconn", ""},
	{"pgtype", "github.com/jackc/pgx/v5/pgtype", ""},
	{"pgx", "github.com/jackc/pgx/v5", ""},
	{"pgxpool", "github.com/jackc/pgx/v5/pgxpool", ""},
	{"testkit", "github.com/stopyellingatme/spinup/kit/testkit", ""},
	{"uuid", "github.com/google/uuid", ""},
}

// bytes renders the file with the package clause and imports, formatted.
func (g *goFile) bytes() ([]byte, error) {
	body := g.b.String()
	var std, other []string
	for _, imp := range knownImports {
		if !usesPackage(body, imp.name) {
			continue
		}
		spec := fmt.Sprintf("%q", imp.path)
		if imp.alias != "" {
			spec = imp.alias + " " + spec
		}
		if strings.Contains(imp.path, ".") {
			other = append(other, spec)
		} else {
			std = append(std, spec)
		}
	}
	for _, path := range g.local {
		name := path[strings.LastIndex(path, "/")+1:]
		if usesPackage(body, name) {
			other = append(other, fmt.Sprintf("%q", path))
		}
	}
	sort.Strings(std)
	sort.Slice(other, func(i, j int) bool { return importPath(other[i]) < importPath(other[j]) })
	var out strings.Builder
	fmt.Fprintf(&out, "package %s\n\n", g.pkg)
	if len(std)+len(other) > 0 {
		out.WriteString("import (\n")
		for _, s := range std {
			out.WriteString("\t" + s + "\n")
		}
		if len(std) > 0 && len(other) > 0 {
			out.WriteString("\n")
		}
		for _, s := range other {
			out.WriteString("\t" + s + "\n")
		}
		out.WriteString(")\n\n")
	}
	out.WriteString(body)
	src, err := format.Source([]byte(out.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated Go code: %w\n%s\nTry: report this generator bug", err, out.String())
	}
	return src, nil
}

func importPath(spec string) string {
	if i := strings.Index(spec, `"`); i >= 0 {
		return spec[i:]
	}
	return spec
}

// usesPackage reports whether body refers to a package by name.
func usesPackage(body, name string) bool {
	return regexp.MustCompile(`(^|[^\w.])` + regexp.QuoteMeta(name) + `\.[A-Za-z_]`).MatchString(body)
}
