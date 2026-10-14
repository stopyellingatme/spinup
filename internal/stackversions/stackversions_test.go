package stackversions

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	stack, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := stack.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte("schema: 1\nverified: today\ngo:\n  toolchain: go1.27.2\n  min: '1.26'\nunknown: value\n"))
	if err == nil || !strings.Contains(err.Error(), "field unknown") {
		t.Fatalf("Parse() error = %v, want unknown field error", err)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	valid := func() Stack {
		return Stack{
			Schema:   1,
			Verified: "2026-10-13",
			Go: struct {
				Toolchain string   "yaml:\"toolchain\""
				Min       string   "yaml:\"min\""
				Notes     []string "yaml:\"notes\""
			}{Toolchain: "go1.27.2", Min: "1.26"},
			GoModules: map[string]string{"example/module": "v1.0.0"},
			GoTools:   map[string]string{"example/tool": "v1.0.0"},
			NPM:       map[string]string{"example": "1.0.0"},
			Apt:       map[string]string{"example": "1.0.0"},
			Tools:     map[string]string{"example": "1.0.0"},
			Images: map[string]Image{
				"example": {Image: "example", Tag: "1.0.0", Digest: "sha256:" + strings.Repeat("a", 64)},
			},
			Binaries: map[string]Binary{
				"example": {Version: "1.0.0", Repo: "example/repo", Assets: map[string]Asset{
					"darwin-arm64": {Name: "example", SHA256: strings.Repeat("a", 64)},
				}},
			},
			Actions: map[string]Action{"example/action": {Tag: "v1", SHA: strings.Repeat("a", 40)}},
		}
	}

	tests := []struct {
		name string
		edit func(*Stack)
		want string
	}{
		{"schema", func(s *Stack) { s.Schema = 2 }, "schema must be 1"},
		{"verified", func(s *Stack) { s.Verified = "" }, "verified must not be empty"},
		{"toolchain", func(s *Stack) { s.Go.Toolchain = "1.27.2" }, "go.toolchain"},
		{"minimum-go", func(s *Stack) { s.Go.Min = "" }, "go.min"},
		{"module-version", func(s *Stack) { s.GoModules["example/module"] = "" }, "go_modules.example/module"},
		{"image-name", func(s *Stack) { image := s.Images["example"]; image.Image = ""; s.Images["example"] = image }, "images.example.image"},
		{"image-tag", func(s *Stack) { image := s.Images["example"]; image.Tag = ""; s.Images["example"] = image }, "images.example.tag"},
		{"image-digest", func(s *Stack) { image := s.Images["example"]; image.Digest = "sha256:bad"; s.Images["example"] = image }, "images.example.digest"},
		{"binary-version", func(s *Stack) { binary := s.Binaries["example"]; binary.Version = ""; s.Binaries["example"] = binary }, "binaries.example.version"},
		{"binary-repo", func(s *Stack) { binary := s.Binaries["example"]; binary.Repo = ""; s.Binaries["example"] = binary }, "binaries.example.repo"},
		{"asset-name", func(s *Stack) {
			binary := s.Binaries["example"]
			asset := binary.Assets["darwin-arm64"]
			asset.Name = ""
			binary.Assets["darwin-arm64"] = asset
			s.Binaries["example"] = binary
		}, "assets.darwin-arm64.name"},
		{"asset-sha", func(s *Stack) {
			binary := s.Binaries["example"]
			asset := binary.Assets["darwin-arm64"]
			asset.SHA256 = "bad"
			binary.Assets["darwin-arm64"] = asset
			s.Binaries["example"] = binary
		}, "assets.darwin-arm64.sha256"},
		{"action-tag", func(s *Stack) {
			action := s.Actions["example/action"]
			action.Tag = ""
			s.Actions["example/action"] = action
		}, "actions.example/action.tag"},
		{"action-sha", func(s *Stack) {
			action := s.Actions["example/action"]
			action.SHA = "bad"
			s.Actions["example/action"] = action
		}, "actions.example/action.sha"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stack := valid()
			test.edit(&stack)
			err := stack.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLookupAndFormatHelpers(t *testing.T) {
	t.Parallel()

	stack := MustLoad()
	if image, err := stack.Image("postgres"); err != nil || image.Ref() != "postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336" {
		t.Fatalf("Image() = %#v, %v", image, err)
	}
	if _, err := stack.NPMVersion("svelte"); err != nil {
		t.Fatalf("NPMVersion() error = %v", err)
	}
	if _, err := stack.GoModule("github.com/spf13/cobra"); err != nil {
		t.Fatalf("GoModule() error = %v", err)
	}
	action, err := stack.Action("actions/checkout")
	if err != nil {
		t.Fatalf("Action() error = %v", err)
	}
	if got, want := action.Uses("actions/checkout"), "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1"; got != want {
		t.Errorf("Uses() = %q, want %q", got, want)
	}
	if _, err := stack.Asset("sops", "darwin-arm64"); err != nil {
		t.Fatalf("Asset() error = %v", err)
	}

	for _, test := range []struct {
		name string
		err  error
		key  string
	}{
		{"image", func() error { _, err := stack.Image("missing"); return err }(), "missing"},
		{"npm", func() error { _, err := stack.NPMVersion("missing"); return err }(), "missing"},
		{"module", func() error { _, err := stack.GoModule("missing"); return err }(), "missing"},
		{"action", func() error { _, err := stack.Action("missing"); return err }(), "missing"},
		{"binary", func() error { _, err := stack.Asset("missing", "darwin-arm64"); return err }(), "missing"},
		{"platform", func() error { _, err := stack.Asset("sops", "missing"); return err }(), "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.err == nil || !strings.Contains(test.err.Error(), test.key) {
				t.Fatalf("lookup error = %v, want key %q", test.err, test.key)
			}
		})
	}
}
