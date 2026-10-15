package fields

import (
	"strings"
	"testing"
)

func TestParseNameFormsAndInflection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		singular string
		plural   string
	}{
		{"blog_post", "blog_post", "blog_posts"},
		{"blog-posts", "blog_post", "blog_posts"},
		{"blogPost", "blog_post", "blog_posts"},
		{"BlogPosts", "blog_post", "blog_posts"},
		{"people", "person", "people"},
		{"children", "child", "children"},
		{"categories", "category", "categories"},
		{"statuses", "status", "statuses"},
		{"addresses", "address", "addresses"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseName(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Snake() != tt.singular || got.PluralSnake() != tt.plural {
				t.Fatalf("snake = %q, plural = %q", got.Snake(), got.PluralSnake())
			}
			if tt.singular == "blog_post" {
				if got.Pascal() != "BlogPost" || got.PluralPascal() != "BlogPosts" {
					t.Fatalf("pascal = %q, plural = %q", got.Pascal(), got.PluralPascal())
				}
				if got.Camel() != "blogPost" || got.PluralCamel() != "blogPosts" {
					t.Fatalf("camel = %q, plural = %q", got.Camel(), got.PluralCamel())
				}
				if got.Kebab() != "blog-post" || got.PluralKebab() != "blog-posts" {
					t.Fatalf("kebab = %q, plural = %q", got.Kebab(), got.PluralKebab())
				}
			}
			if got.Table() != tt.plural {
				t.Fatalf("table = %q", got.Table())
			}
		})
	}
}

func TestParseNameRejectsReservedAndInvalid(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"user", "order", "select", "func", "string", "9lives", "mötley", strings.Repeat("a", 51)} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := ParseName(input)
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("ParseName(%q) error = %v", input, err)
			}
		})
	}
}

func TestTypes(t *testing.T) {
	t.Parallel()

	want := []Type{
		{Name: "string", SQL: "text", GoStore: "string", GoAPI: "string", OpenAPIType: "string", TS: "string"},
		{Name: "text", SQL: "text", GoStore: "string", GoAPI: "string", OpenAPIType: "string", TS: "string"},
		{Name: "int", SQL: "integer", GoStore: "int32", GoAPI: "int32", OpenAPIType: "integer", OpenAPIFormat: "int32", TS: "number"},
		{Name: "bigint", SQL: "bigint", GoStore: "int64", GoAPI: "int64", OpenAPIType: "integer", OpenAPIFormat: "int64", TS: "number"},
		{Name: "bool", SQL: "boolean", GoStore: "bool", GoAPI: "bool", OpenAPIType: "boolean", TS: "boolean"},
		{Name: "float", SQL: "double precision", GoStore: "float64", GoAPI: "float64", OpenAPIType: "number", OpenAPIFormat: "double", TS: "number"},
		{Name: "decimal", SQL: "numeric", GoStore: "string", GoAPI: "string", OpenAPIType: "string", OpenAPIFormat: "decimal", TS: "string"},
		{Name: "time", SQL: "timestamptz", GoStore: "time.Time", GoAPI: "time.Time", OpenAPIType: "string", OpenAPIFormat: "date-time", TS: "string"},
		{Name: "date", SQL: "date", GoStore: "time.Time", GoAPI: "openapi_types.Date", OpenAPIType: "string", OpenAPIFormat: "date", TS: "string"},
		{Name: "uuid", SQL: "uuid", GoStore: "string", GoAPI: "string", OpenAPIType: "string", OpenAPIFormat: "uuid", TS: "string"},
		{Name: "json", SQL: "jsonb", GoStore: "[]byte", GoAPI: "map[string]interface{}", OpenAPIType: "object", TS: "Record<string, unknown>"},
		{Name: "ref", SQL: "bigint", GoStore: "int64", GoAPI: "int64", OpenAPIType: "integer", OpenAPIFormat: "int64", TS: "number"},
		{Name: "enum", SQL: "text", GoStore: "string", GoAPI: "string", OpenAPIType: "string", TS: "string"},
	}
	got := Types()
	if len(got) != len(want) {
		t.Fatalf("Types length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].SQL != want[i].SQL || got[i].GoStore != want[i].GoStore || got[i].GoAPI != want[i].GoAPI || got[i].OpenAPIType != want[i].OpenAPIType || got[i].OpenAPIFormat != want[i].OpenAPIFormat || got[i].TS != want[i].TS || len(got[i].Enum) != 0 || got[i].Ref != "" {
			t.Errorf("Types()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestParseTypes(t *testing.T) {
	t.Parallel()

	args := []string{"title:string", "body:text", "rank:int?", "count:bigint", "done:bool", "ratio:float", "price:decimal", "at:time", "due:date?", "key:uuid", "data:json?", "author:ref:person", "state:enum:draft|in_review|published"}
	got, err := Parse(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(args) {
		t.Fatalf("fields = %d, want %d", len(got), len(args))
	}
	if !got[2].Nullable || got[2].Type.Name != "int" {
		t.Fatalf("nullable int = %#v", got[2])
	}
	if got[11].Column() != "author_id" || got[11].Type.Ref != "person" || got[11].Type.SQL != "bigint REFERENCES people(id)" {
		t.Fatalf("ref = %#v", got[11])
	}
	if got[12].Type.Enum[1] != "in_review" || got[12].Type.TS != "'draft' | 'in_review' | 'published'" {
		t.Fatalf("enum = %#v", got[12])
	}
	if got[10].Column() != "data" {
		t.Fatalf("column = %q", got[10].Column())
	}
}

func TestParseEveryTypeNullableAndRequired(t *testing.T) {
	t.Parallel()

	types := []string{
		"string", "text", "int", "bigint", "bool", "float", "decimal", "time", "date", "uuid", "json",
		"ref:person", "enum:draft|published",
	}
	for _, typeText := range types {
		t.Run(typeText, func(t *testing.T) {
			t.Parallel()
			for _, suffix := range []string{"", "?"} {
				got, err := Parse([]string{"value:" + typeText + suffix})
				if err != nil {
					t.Fatal(err)
				}
				if got[0].Nullable != (suffix == "?") {
					t.Fatalf("nullable = %t for %q", got[0].Nullable, typeText+suffix)
				}
			}
		})
	}
}

func TestParseRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"id:string"},
		{"created_at:time"},
		{"title:string", "title:text"},
		{"title:unknown"},
		{"state:enum:Draft|published"},
		{"state:enum:draft|draft"},
		{"author:ref:"},
		{"title:string??"},
		{"invalid"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, ","), func(t *testing.T) {
			t.Parallel()
			_, err := Parse(args)
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("Parse(%q) error = %v", args, err)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{"title:string", "author:ref:user", "state:enum:draft|published", "bad", "due:date?"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, arg string) {
		_, _ = Parse([]string{arg})
	})
}
