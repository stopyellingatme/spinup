package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestSuggestAlternatives(t *testing.T) {
	t.Parallel()

	sizes := []digitalocean.Size{
		{Slug: "s-1vcpu-2gb", VCPUs: 1, Memory: 2048, PriceMonthly: 12, Available: true, Regions: []string{"nyc3"}},
		{Slug: "s-2vcpu-4gb-amd", VCPUs: 2, Memory: 4096, PriceMonthly: 24, Available: true, Regions: []string{"nyc3", "sfo3"}},
		{Slug: "s-4vcpu-8gb", VCPUs: 4, Memory: 8192, PriceMonthly: 48, Available: true, Regions: []string{"nyc3", "sfo3", "fra1"}},
		{Slug: "s-8vcpu-16gb", VCPUs: 8, Memory: 16384, PriceMonthly: 96, Available: true, Regions: []string{"nyc3"}},
		{Slug: "s-2vcpu-4gb", VCPUs: 2, Memory: 4096, PriceMonthly: 24, Available: true, Regions: []string{"sfo3", "fra1"}},
		{Slug: "s-2vcpu-4gb-old", VCPUs: 2, Memory: 4096, PriceMonthly: 20, Available: false, Regions: []string{"nyc3"}},
	}
	regions := []digitalocean.Region{
		{Slug: "fra1", Available: true, Sizes: []string{"s-2vcpu-4gb", "s-4vcpu-8gb"}},
		{Slug: "nyc3", Available: true, Sizes: []string{"s-1vcpu-2gb", "s-2vcpu-4gb-amd", "s-4vcpu-8gb", "s-8vcpu-16gb"}},
		{Slug: "sfo3", Available: true, Sizes: []string{"s-2vcpu-4gb", "s-2vcpu-4gb-amd", "s-4vcpu-8gb"}},
		{Slug: "tor1", Available: false, Sizes: []string{"s-2vcpu-4gb"}},
	}

	gotSizes, gotRegions := suggestAlternatives(sizes, regions, sizeWant{Slug: "s-2vcpu-4gb", Region: "nyc3"}, 2, minMemoryMiB)
	if got := sizeSlugs(gotSizes); !reflect.DeepEqual(got, []string{"s-2vcpu-4gb-amd", "s-4vcpu-8gb", "s-8vcpu-16gb"}) {
		t.Errorf("size suggestions = %v", got)
	}
	if want := []string{"fra1", "sfo3"}; !reflect.DeepEqual(gotRegions, want) {
		t.Errorf("region suggestions = %v, want %v", gotRegions, want)
	}
}

func TestPreflightScaffoldsNewEnvironment(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "deploy", "environments", "staging.toml")
	if err := os.MkdirAll(filepath.Dir(staging), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging, []byte("smoke_urls = [\"/healthz\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	r := &run{base: base{root: root, env: "preview", o: Options{Out: out}}}
	if err := r.loadEnvironmentFile(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "deploy", "environments", "preview.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "smoke_urls = [\"/healthz\"]\n"; got != want {
		t.Errorf("preview environment = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "Created deploy/environments/preview.toml from staging") {
		t.Errorf("output = %q", out.String())
	}
	if !strings.Contains(strings.Join(r.ownFiles(), ","), "deploy/environments/preview.toml") {
		t.Errorf("own files = %v", r.ownFiles())
	}
}

func TestPreflightPlanLoadsNewEnvironmentWithoutWriting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	staging := filepath.Join(root, "deploy", "environments", "staging.toml")
	if err := os.MkdirAll(filepath.Dir(staging), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging, []byte("smoke_urls = [\"/healthz\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	r := &run{base: base{root: root, env: "preview", o: Options{Plan: true, Out: out}}}
	if err := r.loadEnvironmentFile(); err != nil {
		t.Fatal(err)
	}
	if got, want := r.envFile.SmokeURLs, []string{"/healthz"}; !reflect.DeepEqual(got, want) {
		t.Errorf("environment settings = %v, want %v", got, want)
	}
	if !r.scaffoldedEnv {
		t.Error("plan did not mark the environment as scaffolded")
	}
	path := filepath.Join(root, "deploy", "environments", "preview.toml")
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("preview environment exists after plan: %v", err)
	}
	if !strings.Contains(out.String(), "Would create deploy/environments/preview.toml from staging") {
		t.Errorf("output = %q", out.String())
	}
}

func TestCheckCleanRecognizesScaffoldedEnvironment(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		status       string
		envData      string
		untracked    bool
		wantClean    bool
		wantScaffold bool
	}{
		{
			name:         "untracked staging copy",
			status:       "?? deploy/environments/preview.toml\n",
			envData:      "smoke_urls = [\"/healthz\"]\n",
			untracked:    true,
			wantClean:    true,
			wantScaffold: true,
		},
		{
			name:      "tracked staging copy",
			status:    " M deploy/environments/preview.toml\n",
			envData:   "smoke_urls = [\"/healthz\"]\n",
			wantClean: false,
		},
		{
			name:      "untracked changed copy",
			status:    "?? deploy/environments/preview.toml\n",
			envData:   "smoke_urls = [\"/readyz\"]\n",
			untracked: true,
			wantClean: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			dir := filepath.Join(root, "deploy", "environments")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			staging := []byte("smoke_urls = [\"/healthz\"]\n")
			if err := os.WriteFile(filepath.Join(dir, "staging.toml"), staging, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "preview.toml"), []byte(tt.envData), 0644); err != nil {
				t.Fatal(err)
			}
			fake := &execFake{porcelain: tt.status}
			var runner runexec.Runner = fake
			if tt.untracked {
				runner = retryScaffoldExec{execFake: fake}
			}
			r := newRun(Options{Out: &bytes.Buffer{}}, Deps{Exec: runner})
			r.root, r.env = root, "preview"

			if err := r.loadEnvironmentFile(); err != nil {
				t.Fatal(err)
			}
			err := r.checkClean(context.Background())
			if (err == nil) != tt.wantClean {
				t.Fatalf("checkClean() = %v, want clean = %v", err, tt.wantClean)
			}
			if r.scaffoldedEnv != tt.wantScaffold {
				t.Errorf("scaffolded environment = %v, want %v", r.scaffoldedEnv, tt.wantScaffold)
			}
			if !tt.wantClean {
				return
			}
			if _, err := r.commitFiles(context.Background()); err != nil {
				t.Fatalf("commitFiles() = %v", err)
			}
			adds := fake.gitCalls("add")
			if len(adds) != 1 || !strings.Contains(strings.Join(adds[0], " "), "deploy/environments/preview.toml") {
				t.Fatalf("git add calls = %v, want the scaffolded environment file", adds)
			}
		})
	}
}

type retryScaffoldExec struct {
	*execFake
}

func (e retryScaffoldExec) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "ls-files" {
		_, _ = e.execFake.Run(ctx, c)
		return runexec.Result{ExitCode: 1}, &runexec.ExitError{Cmd: c.String(), Code: 1}
	}
	return e.execFake.Run(ctx, c)
}

func TestCheckSizeExplainsUnavailableChoices(t *testing.T) {
	t.Parallel()

	sizes := []digitalocean.Size{
		{Slug: "s-2vcpu-4gb", VCPUs: 2, Memory: 4096, PriceMonthly: 24, Available: true, Regions: []string{"fra1", "sfo3"}},
		{Slug: "s-2vcpu-4gb-amd", VCPUs: 2, Memory: 4096, PriceMonthly: 22, Available: true, Regions: []string{"nyc3"}},
		{Slug: "s-4vcpu-8gb", VCPUs: 4, Memory: 8192, PriceMonthly: 48, Available: true, Regions: []string{"nyc3"}},
	}
	regions := []digitalocean.Region{
		{Slug: "fra1", Available: true, Sizes: []string{"s-2vcpu-4gb"}},
		{Slug: "nyc3", Available: true, Sizes: []string{"s-2vcpu-4gb-amd", "s-4vcpu-8gb"}},
		{Slug: "sfo3", Available: true, Sizes: []string{"s-2vcpu-4gb"}},
	}
	r := &run{base: base{d: Deps{Cloud: catalogClient(sizes, regions)}, env: "preview"}, envFile: deploy.EnvFile{}, size: "s-2vcpu-4gb", region: "nyc3"}
	err := r.checkSize(context.Background())
	ue := uiErr(t, err)
	for _, want := range []string{"--size s-2vcpu-4gb-amd ($22.00/month)", "--region fra1, sfo3"} {
		if !strings.Contains(ue.Try, want) {
			t.Errorf("Try = %q, want %q", ue.Try, want)
		}
	}
}

func TestCheckSizeExplainsInvalidRegion(t *testing.T) {
	t.Parallel()

	sizes := []digitalocean.Size{{Slug: "s-2vcpu-4gb", VCPUs: 2, Memory: 4096, Available: true, Regions: []string{"nyc3"}}}
	regions := []digitalocean.Region{{Slug: "nyc3", Available: true}, {Slug: "tor1", Available: false}, {Slug: "sfo3", Available: true}}
	r := &run{base: base{d: Deps{Cloud: catalogClient(sizes, regions)}, env: "preview"}, envFile: deploy.EnvFile{}, size: "s-2vcpu-4gb", region: "invalid"}
	err := r.checkSize(context.Background())
	ue := uiErr(t, err)
	if got, want := ue.Try, "pass --region with one of nyc3, sfo3"; !strings.Contains(got, want) {
		t.Errorf("Try = %q, want %q", got, want)
	}
}

func sizeSlugs(sizes []digitalocean.Size) []string {
	out := make([]string, len(sizes))
	for i, size := range sizes {
		out[i] = size.Slug
	}
	return out
}

func catalogClient(sizes []digitalocean.Size, regions []digitalocean.Region) *digitalocean.Client {
	client := digitalocean.New("https://catalog.test", func(context.Context) (string, error) { return "token", nil })
	client.HTTP = &http.Client{Transport: catalogTransport{sizes: sizes, regions: regions}}
	return client
}

type catalogTransport struct {
	sizes   []digitalocean.Size
	regions []digitalocean.Region
}

func (t catalogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	switch req.URL.Path {
	case "/v2/sizes":
		body, _ = json.Marshal(struct {
			Sizes []digitalocean.Size `json:"sizes"`
			Meta  struct {
				Total int `json:"total"`
			} `json:"meta"`
		}{Sizes: t.sizes})
	case "/v2/regions":
		body, _ = json.Marshal(struct {
			Regions []digitalocean.Region `json:"regions"`
			Meta    struct {
				Total int `json:"total"`
			} `json:"meta"`
		}{Regions: t.regions})
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"message":"not found"}`)), Header: make(http.Header), Request: req}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

func TestCommitFilesStagesScaffoldedEnvironment(t *testing.T) {
	f := newFixture(t, nil)
	if err := os.WriteFile(filepath.Join(f.root, "deploy", "environments", "preview.toml"), []byte("smoke_urls = [\"/healthz\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.exec.staged = "deploy/environments/preview.toml\n"
	r := newRun(f.opts, f.deps)
	r.root, r.env, r.scaffoldedEnv = f.root, "preview", true
	ok, err := r.commitFiles(context.Background())
	if err != nil || !ok {
		t.Fatalf("commitFiles() = %v, %v", ok, err)
	}
	adds := f.exec.gitCalls("add")
	if len(adds) != 1 || !strings.Contains(strings.Join(adds[0], " "), "deploy/environments/preview.toml") {
		t.Fatalf("git add calls = %v, want the scaffolded environment file", adds)
	}
}
