package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"unicode"

	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

var update = flag.Bool("update", false, "regenerate the R0.2 Go oracle fixtures")

type fixtureFile struct {
	Pack   string `json:"pack"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Mode   string `json:"mode"`
}
type goldenCase struct {
	Name    string         `json:"name"`
	Answers map[string]any `json:"answers"`
	Files   []fixtureFile  `json:"files"`
}
type filterCase struct {
	Source   string         `json:"source"`
	Data     map[string]any `json:"data"`
	Expected string         `json:"expected"`
}
type unicodeData struct {
	Letter, Digit, Upper, Lower, Print [][2]int
	ToLower, ToUpper                   map[int]int
}

func TestOracleFixtures(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	dir := filepath.Join(root, "spikes-rs/r0-02-minijinja/fixtures")
	check := func(name string, value any) {
		t.Helper()
		if *update {
			if err := writeJSON(filepath.Join(dir, name), value); err != nil {
				t.Fatal(err)
			}
			return
		}
		got, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		data, err := fs.ReadFile(os.DirFS(dir), name)
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := json.Unmarshal(data, &want); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		// Compare decoded JSON. Object keys use different encoders for integer maps.
		var decoded any
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatal(err)
		}
		got, err = json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(encoded) {
			t.Errorf("stale oracle %s. Regenerate with -update", name)
		}
	}
	check("golden.json", goldenCases(t))
	check("filters.json", filterCases(t))
	check("stack.json", stackversions.MustLoad())
	u := unicodeData{ToLower: map[int]int{}, ToUpper: map[int]int{}}
	for _, spec := range []struct {
		dst  *[][2]int
		pred func(rune) bool
	}{
		{&u.Letter, unicode.IsLetter}, {&u.Digit, unicode.IsDigit}, {&u.Upper, unicode.IsUpper},
		{&u.Lower, unicode.IsLower}, {&u.Print, strconv.IsPrint},
	} {
		for r := 0; r <= unicode.MaxRune; r++ {
			if !spec.pred(rune(r)) {
				continue
			}
			ranges := *spec.dst
			if len(ranges) > 0 && ranges[len(ranges)-1][1] == r-1 {
				ranges[len(ranges)-1][1] = r
			} else {
				ranges = append(ranges, [2]int{r, r})
			}
			*spec.dst = ranges
		}
	}
	for r := 0; r <= unicode.MaxRune; r++ {
		if v := unicode.ToLower(rune(r)); int(v) != r {
			u.ToLower[r] = int(v)
		}
		if v := unicode.ToUpper(rune(r)); int(v) != r {
			u.ToUpper[r] = int(v)
		}
	}
	check("unicode.json", u)
}

func goldenCases(t *testing.T) []goldenCase {
	t.Helper()
	var cases []goldenCase
	add := func(name, packs string, extra map[string]any, selectPath func(string) (string, bool)) {
		t.Helper()
		var loaded []*templates.Pack
		for _, p := range strings.Fields(packs) {
			pack, err := templates.Load(templates.Embedded(), p)
			if err != nil {
				t.Fatal(err)
			}
			loaded = append(loaded, pack)
		}
		answers := map[string]any{"app_name": "todo", "module": "example.com/todo"}
		for k, v := range extra {
			answers[k] = v
		}
		answers, err := render.ValidateAnswers(loaded, answers)
		if err != nil {
			t.Fatal(err)
		}
		c := goldenCase{Name: name, Answers: answers}
		for i, pack := range loaded {
			packName := strings.Fields(packs)[i]
			if packName == "api" {
				continue
			}
			for _, f := range pack.Files {
				enabled, err := f.Matches(answers)
				if err != nil {
					t.Fatal(err)
				}
				if !enabled {
					continue
				}
				// Resolve the Go path for selection only. Rust renders the converted path.
				p := &templates.Pack{Name: "path", FS: fstest.MapFS{"path.tmpl": {Data: []byte(f.Path)}},
					Files: []templates.FileSpec{{Path: "path.txt", Source: "path.tmpl", Class: "managed"}}}
				plan, err := render.Render(render.Input{Packs: []*templates.Pack{p}, Answers: answers})
				if err != nil {
					t.Fatal(err)
				}
				resolved := strings.TrimSuffix(string(plan.Files[0].Data), "\n")
				selected, ok := selectPath(resolved)
				if !ok {
					continue
				}
				path, _, err := convert("path", f.Path, "[[", "]]")
				if err != nil {
					t.Fatal(err)
				}
				// Selection can strip a fixed prefix, as the tofu golden does.
				if selected != resolved {
					prefix := strings.TrimSuffix(resolved, selected)
					path = strings.TrimPrefix(path, prefix)
				}
				mode, err := f.Permissions()
				if err != nil {
					t.Fatal(err)
				}
				c.Files = append(c.Files, fixtureFile{packName, f.Source, path, fmt.Sprintf("%04o", mode)})
			}
		}
		c.Answers["TemplateVersion"] = "0.0.0-test"
		cases = append(cases, c)
	}
	all := func(s string) (string, bool) { return s, true }
	prefix := func(p string) func(string) (string, bool) {
		return func(s string) (string, bool) { return s, strings.HasPrefix(s, p) }
	}
	add("base", "base", nil, all)
	add("web", "base api web", nil, func(s string) (string, bool) { return s, strings.HasPrefix(s, "web/") || s == "deploy/Dockerfile.web" })
	for _, mode := range []string{"node", "embed"} {
		for _, pm := range []string{"pnpm", "bun"} {
			for _, api := range []string{"openapi-fetch", "heyapi"} {
				add("web-variants/"+mode+"-"+pm+"-"+api, "base api web ci deploy", map[string]any{"frontend_mode": mode, "package_manager": pm, "api_client": api}, func(s string) (string, bool) {
					return s, strings.HasPrefix(s, "web/src/lib/api/") || s == "web/package.json" || s == "web/pnpm-lock.yaml" || s == "web/pnpm-workspace.yaml" || s == "web/bun.lock" || s == "deploy/Dockerfile.web" || s == ".github/workflows/ci.yml" || s == ".github/workflows/e2e.yml"
				})
			}
		}
	}
	add("web-embed", "base api web deploy", map[string]any{"frontend_mode": "embed"}, func(s string) (string, bool) {
		return s, strings.HasPrefix(s, "web/") || strings.HasPrefix(s, "deploy/")
	})
	for _, tc := range []struct {
		name, modules string
		web, push     bool
	}{
		{"api-only", "ci", false, false}, {"full", "ci,deploy,backups", true, false}, {"deploy-on-push", "ci,deploy,backups", true, true},
	} {
		packs := "ci"
		if tc.web {
			packs = "ci base api web"
		}
		add("ci/"+tc.name, packs, map[string]any{"modules": tc.modules, "ci_deploy_on_push": tc.push}, prefix(".github/"))
	}
	for _, auth := range []bool{false, true} {
		name, modules, packs := "no-auth", "ci", "base api web"
		if auth {
			name, modules, packs = "auth", "ci,auth", "base api web auth"
		}
		add("ci/web-scaffold/"+name, packs, map[string]any{"modules": modules}, func(s string) (string, bool) { return s, s == "web/e2e/smoke.spec.ts" || s == "web/vite.config.js" })
	}
	add("deploy", "base api web deploy", map[string]any{"modules": "ci,deploy"}, func(s string) (string, bool) {
		return s, strings.HasPrefix(s, "deploy/") && s != "deploy/compose.dev.yml" && s != "deploy/Dockerfile.api" && s != "deploy/Dockerfile.web"
	})
	for _, p := range []string{"lite", "full"} {
		add("monitoring/"+p, "base api web deploy monitoring", map[string]any{"modules": "ci,deploy,monitoring", "monitoring_profile": p}, prefix("monitoring/"))
	}
	add("backups", "base api web deploy backups", map[string]any{"modules": "ci,deploy,backups"}, func(s string) (string, bool) {
		return s, s == "deploy/compose.backups.yml" || s == "deploy/pgbackrest/pgbackrest.conf" || s == "deploy/backups/run.sh" || strings.HasPrefix(s, "deploy/systemd/")
	})
	add("secrets", "base api secrets", map[string]any{"modules": "ci,secrets"}, func(s string) (string, bool) { return s, s == ".sops.yaml" || s == "secrets/.gitkeep" })
	add("tofu", "base api deploy tofu", map[string]any{"modules": "deploy,tofu"}, func(s string) (string, bool) {
		return strings.TrimPrefix(s, "deploy/infra/tofu/"), strings.HasPrefix(s, "deploy/infra/tofu/")
	})
	add("_sample", "_sample", map[string]any{"app_name": "demo", "db": "postgres", "auth": true, "modules": []string{"jobs", "mail"}, "greeting": "Hello"}, all)
	return cases
}

func filterCases(t *testing.T) []filterCase {
	t.Helper()
	var cases []filterCase
	add := func(source string, data map[string]any) {
		t.Helper()
		p := &templates.Pack{Name: "oracle", FS: fstest.MapFS{"value.tmpl": {Data: []byte(source)}},
			Files: []templates.FileSpec{{Path: "out.txt", Source: "value.tmpl", Class: "managed"}}}
		plan, err := render.Render(render.Input{Packs: []*templates.Pack{p}, Answers: data, Stack: stackversions.MustLoad()})
		if err != nil {
			t.Fatal(err)
		}
		converted, _, err := convert("filter", source, "[[", "]]")
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, filterCase{converted, data, string(plan.Files[0].Data)})
	}
	for _, s := range []string{"", "HTTPServer2ID", "myURL42Value", "already_snake-case.dot space", "éÉÉcole_Σςİıßﬃǅ", "中文名東京7ÅÖ", "x\u0301Y_ⅠⅫ²a_\u0345", "hello\n\t\r\x00\x01\a\b\f\v\\\"<> &\u007f\u0085\u00a0\u200b\u2028\u2029😀"} {
		for _, fn := range []string{"quote", "lower", "upper", "snake", "kebab", "camel", "pascal"} {
			add("[[ "+fn+" .value ]]", map[string]any{"value": s})
		}
	}
	for _, v := range []any{nil, false, true, 0, 1, 0.0, -1.5, "", "x", []string{}, []string{"x"}, map[string]any{}, map[string]any{"a": 1}} {
		add("[[ default \"fallback\" .value | printf \"%v\" ]]", map[string]any{"value": v})
	}
	add("[[ default \"missing\" (index . \"missing\") ]]", map[string]any{})
	add("[[ index .map \"key\" ]]", map[string]any{"map": map[string]any{"key": "found"}})
	add("[[ index .list 1 ]]", map[string]any{"list": []string{"zero", "one"}})
	add("[[ printf \"%.2s\" .value ]]", map[string]any{"value": "é😀three"})
	add("[[ printf \"%q\" .value ]]", map[string]any{"value": "quote\n"})
	add("[[ join \",\" .list ]]", map[string]any{"list": []string{"zero", "one"}})
	add("[[ indent 2 .value ]]", map[string]any{"value": "one\ntwo\n"})
	for _, expr := range []string{"has \"x\" .list", "image \"postgres\"", "npm \"svelte\"", "gomod \"github.com/stopyellingatme/spinup/kit\"", "uses \"actions/checkout\""} {
		add("[[ "+expr+" ]]", map[string]any{"list": []string{"x", "y"}})
	}
	add("[[ range $i, $v := .list ]][[ if $i ]],[[ end ]][[ $v ]]/[[ $.root ]][[ else ]]empty[[ end ]]", map[string]any{"list": []string{"a", "b"}, "root": "R"})
	add("[[ with index . \"missing\" ]]wrong[[ else ]]absent[[ end ]]", map[string]any{})
	add("[[ $pg := \"default\" ]][[ if .yes ]][[ $pg = \"changed\" ]][[ end ]][[ $pg ]]", map[string]any{"yes": true})

	add("[[ index . \"missing\" ]]", map[string]any{})
	add("[[ range .map ]][[ . ]][[ else ]]empty[[ end ]]", map[string]any{"map": map[string]any{"z": "last", "a": "first"}})
	add("[[ range .list ]]wrong[[ else ]]empty[[ end ]]", map[string]any{"list": []string{}})
	add("[[ has \"x\" .list ]]", map[string]any{"list": nil})
	add("[[ has \"x\" .list ]]", map[string]any{"list": []string{"y"}})
	add("[[ with .user ]][[ .Name ]]/[[ $.root ]][[ end ]]", map[string]any{"user": map[string]any{"Name": "nested"}, "root": "R"})
	add("[[ quote .value ]]", map[string]any{"value": "\U000e0001\U0010ffff"})
	return cases
}
