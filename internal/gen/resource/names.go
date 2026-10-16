package resource

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/stopyellingatme/spinup/internal/gen/fields"
)

// apiName returns the Go field name that oapi-codegen gives a property. It
// follows the default name normalizer (ToCamelCase): a letter at the start or
// after a separator becomes upper case, and separators are dropped.
func apiName(property string) string {
	var b strings.Builder
	capNext := true
	for _, r := range property {
		switch {
		case unicode.IsUpper(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsLower(r):
			if capNext {
				r = unicode.ToUpper(r)
			}
			b.WriteRune(r)
		}
		capNext = r == '_' || r == '-' || r == ' ' || r == '.'
	}
	return b.String()
}

// storeName returns the Go name that sqlc gives a table, column, or
// parameter: each snake case word starts with an upper case letter, and the
// word "id" becomes "ID" (the default sqlc initialism).
func storeName(snake string) string {
	var b strings.Builder
	for _, word := range strings.Split(snake, "_") {
		if word == "id" {
			b.WriteString("ID")
			continue
		}
		if word != "" {
			b.WriteString(strings.ToUpper(word[:1]) + word[1:])
		}
	}
	return b.String()
}

// storeStruct returns the name of the model struct that sqlc generates for a
// table. sqlc singularizes the table name with its own rules, which can differ
// from the resource name.
func storeStruct(table string) string {
	return storeName(sqlcSingular(table))
}

type inflection struct {
	find    *regexp.Regexp
	replace string
}

// The singular rules of github.com/jinzhu/inflection v1.0.0 (MIT), which sqlc
// uses for model names, in the order that library applies them. Only the
// lower-case rules matter, because table names are lower-case snake case.
var (
	sqlcUncountable = []string{"equipment", "information", "rice", "money", "species", "series", "fish", "sheep", "jeans", "police"}
	sqlcIrregular   = [][2]string{{"people", "person"}, {"men", "man"}, {"children", "child"}, {"sexes", "sex"}, {"moves", "move"}, {"mombies", "mombie"}}
	sqlcSingularOf  = compileSingular([][2]string{
		{"s$", ""},
		{"(ss)$", "${1}"},
		{"(n)ews$", "${1}ews"},
		{"([ti])a$", "${1}um"},
		{"((a)naly|(b)a|(d)iagno|(p)arenthe|(p)rogno|(s)ynop|(t)he)(sis|ses)$", "${1}sis"},
		{"(^analy)(sis|ses)$", "${1}sis"},
		{"([^f])ves$", "${1}fe"},
		{"(hive)s$", "${1}"},
		{"(tive)s$", "${1}"},
		{"([lr])ves$", "${1}f"},
		{"([^aeiouy]|qu)ies$", "${1}y"},
		{"(s)eries$", "${1}eries"},
		{"(m)ovies$", "${1}ovie"},
		{"(c)ookies$", "${1}ookie"},
		{"(x|ch|ss|sh)es$", "${1}"},
		{"^(m|l)ice$", "${1}ouse"},
		{"(bus)(es)?$", "${1}"},
		{"(o)es$", "${1}"},
		{"(shoe)s$", "${1}"},
		{"(cris|test)(is|es)$", "${1}is"},
		{"^(a)x[ie]s$", "${1}xis"},
		{"(octop|vir)(us|i)$", "${1}us"},
		{"(alias|status)(es)?$", "${1}"},
		{"^(ox)en", "${1}"},
		{"(vert|ind)ices$", "${1}ex"},
		{"(matr)ices$", "${1}ix"},
		{"(quiz)zes$", "${1}"},
		{"(database)s$", "${1}"},
	})
)

func compileSingular(rules [][2]string) []inflection {
	// The library tries the most recently added rule first.
	compiled := make([]inflection, 0, len(rules))
	for i := len(rules) - 1; i >= 0; i-- {
		compiled = append(compiled, inflection{find: regexp.MustCompile(rules[i][0]), replace: rules[i][1]})
	}
	return compiled
}

// sqlcSingular returns the singular form of a table name as sqlc v1.31
// computes it, including the corrections that sqlc adds to the library.
func sqlcSingular(table string) string {
	switch table {
	case "campus", "meta", "metadata":
		return table
	case "calories":
		return "calorie"
	case "waves":
		return "wave"
	}
	for _, word := range sqlcUncountable {
		if table == word {
			return table
		}
	}
	for _, pair := range sqlcIrregular {
		if strings.HasSuffix(table, pair[0]) {
			return strings.TrimSuffix(table, pair[0]) + pair[1]
		}
	}
	for _, rule := range sqlcSingularOf {
		if rule.find.MatchString(table) {
			return rule.find.ReplaceAllString(table, rule.replace)
		}
	}
	return table
}

var moduleLine = regexp.MustCompile(`(?m)^module\s+"?([^"\s]+)"?\s*$`)

// apiModule reads the Go module path of the api/ directory.
func apiModule(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "api", "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read api/go.mod: %w\nTry: run the generator in a spinup project with an API", err)
	}
	m := moduleLine.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("api/go.mod has no module line\nTry: add a module line to api/go.mod")
	}
	return string(m[1]), nil
}

// reservedResourceName protects the API reference routes.
func reservedResourceName(name fields.Name) bool {
	switch name.Snake() {
	case "doc", "docs", "openapi", "openapis":
		return true
	}
	return name.PluralSnake() == "docs" || name.PluralSnake() == "openapi"
}
