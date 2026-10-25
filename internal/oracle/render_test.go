package oracle

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// renderFunction uses the product renderer because its function map is private.
// Remove only the final newline that Render adds to text.
func renderFunction(t *testing.T, function, input string) string {
	t.Helper()
	pack := &templates.Pack{Name: "oracle", FS: fstest.MapFS{
		"value.tmpl": {Data: []byte("[[ " + function + " .value ]]END")},
	}, Files: []templates.FileSpec{{Path: "value.txt", Source: "value.tmpl", Class: "managed"}}}
	plan, err := render.Render(render.Input{Packs: []*templates.Pack{pack}, Answers: map[string]any{"value": input}})
	if err != nil {
		t.Fatal(err)
	}
	output := strings.TrimSuffix(string(plan.Files[0].Data), "\n")
	value, ok := strings.CutSuffix(output, "END")
	if !ok {
		t.Fatalf("unexpected render suffix: %q", output)
	}
	return value
}

type wordsCase struct {
	Input  string `json:"input"`
	Lower  string `json:"lower"`
	Upper  string `json:"upper"`
	Snake  string `json:"snake"`
	Kebab  string `json:"kebab"`
	Camel  string `json:"camel"`
	Pascal string `json:"pascal"`
}

func wordsCases(t *testing.T) []wordsCase {
	t.Helper()
	inputs := []string{"", "word", "helloWorld", "HTTPServer", "XMLHTTPAPI", "getHTTPResponse", "userID", "userID2FA", "version2HTTPServer", "123abc", "123ABC", "snake_case", "kebab-case", " dots.and/slashes\\tabs\there ", "__--..", "two  words", "écoleÉté", "ÉCOLEÉté", "Straße", "İstanbul", "Σίσυφος", "Kelvin", "中文API名字", "a\u0301B", "a😀B", "a\x00B"}
	cases := make([]wordsCase, 0, len(inputs))
	for _, input := range inputs {
		cases = append(cases, wordsCase{input,
			renderFunction(t, "lower", input), renderFunction(t, "upper", input),
			renderFunction(t, "snake", input), renderFunction(t, "kebab", input),
			renderFunction(t, "camel", input), renderFunction(t, "pascal", input)})
	}
	return cases
}

type byteQuoteCase struct {
	Input    []byte `json:"input"`
	Expected string `json:"expected"`
}

func extraQuoteCases(t *testing.T) []byteQuoteCase {
	t.Helper()
	inputs := [][]byte{[]byte(""), []byte("a\r\nb\n\n"), []byte("\U00010000\U0010ffff\ufffe\uffff"), []byte("\u202a\u202e\u2066\u2069\u00ad"), {0xc0, 0xaf}, {0xed, 0xa0, 0x80}, {0xf4, 0x90, 0x80, 0x80}, {0xe2, 0x82}, {0x80, 0xff, 0xfe}, []byte("valid \ufffd")}
	// Raw bytes differ from the Unicode code points in the R0 quote fixtures.
	for b := 0; b <= 255; b++ {
		inputs = append(inputs, []byte{byte(b)})
	}
	cases := make([]byteQuoteCase, 0, len(inputs))
	for _, input := range inputs {
		cases = append(cases, byteQuoteCase{input, renderFunction(t, "quote", string(input))})
	}
	return cases
}
