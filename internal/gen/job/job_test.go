package job_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/job"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestParseName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, snake, pascal, err string
	}{
		{in: "send_digest", snake: "send_digest", pascal: "SendDigest"},
		{in: "send-digest", snake: "send_digest", pascal: "SendDigest"},
		{in: "SendDigest", snake: "send_digest", pascal: "SendDigest"},
		{in: "sendDigest", snake: "send_digest", pascal: "SendDigest"},
		{in: "process_payments", snake: "process_payments", pascal: "ProcessPayments"},
		{in: "sync2", snake: "sync2", pascal: "Sync2"},
		{in: "", err: "use letters"},
		{in: "2fast", err: "starts with a digit"},
		{in: "send digest", err: "use letters"},
		{in: "register", err: "reserved"},
		{in: "Deps", err: "reserved"},
		{in: "load_test", err: "_test"},
		{in: "routes_gen", err: "_gen"},
		{in: "sync_linux", err: "GOOS or GOARCH"},
		{in: "sync_arm64", err: "GOOS or GOARCH"},
		{in: "sync_linux_arm64", err: "GOOS or GOARCH"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := job.ParseName(tt.in)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || !strings.Contains(err.Error(), "\nTry: ") {
					t.Fatalf("ParseName(%q) err = %v, want %q and a Try line", tt.in, err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Snake != tt.snake || got.Pascal != tt.pascal {
				t.Fatalf("ParseName(%q) = %+v", tt.in, got)
			}
		})
	}
}

func TestPlanRefusesExistingPascalName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "api", "internal", "jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "send_2fa.go"), []byte(`package jobs

type Send2faArgs struct{}

func (Send2faArgs) Kind() string { return "send_2fa" }

func Send2fa(d Deps) any { return nil }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := generator(t).Run(context.Background(), gen.Input{
		Root: root,
		Lock: lock("base", "api", "jobs"),
		Args: []string{"send2fa"},
	})
	if err == nil || !strings.Contains(err.Error(), "Send2fa") || !strings.Contains(err.Error(), "Try: use a different job name") {
		t.Fatalf("err = %v", err)
	}
}

func lock(packs ...string) *manifest.Lock {
	return &manifest.Lock{Packs: packs, Answers: map[string]any{"module": "example.com/todo"}}
}

func generator(t *testing.T) gen.Generator {
	t.Helper()
	g, ok := registry.Lookup("job")
	if !ok {
		t.Fatal("the job generator is not registered")
	}
	return g
}

func TestRefusesWithoutJobsPack(t *testing.T) {
	t.Parallel()
	_, err := generator(t).Run(context.Background(), gen.Input{Root: t.TempDir(), Lock: lock("base", "api", "ci"), Args: []string{"send_digest"}})
	if err == nil || !strings.Contains(err.Error(), "Try: spinup module add jobs") {
		t.Fatalf("err = %v", err)
	}
}

func TestArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"a", "b"}} {
		_, err := generator(t).Run(context.Background(), gen.Input{Root: t.TempDir(), Lock: lock("jobs"), Args: args})
		if err == nil || !strings.Contains(err.Error(), "Try: spinup g job send_digest") {
			t.Errorf("args %q: err = %v", args, err)
		}
	}
}

func TestPlan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "api", "internal", "jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An existing job, a type with Kind but no worker function, and a test file.
	files := map[string]string{
		"archive.go":      "package jobs\n\ntype ArchiveArgs struct{}\n\nfunc (ArchiveArgs) Kind() string { return \"archive\" }\n\nfunc Archive(d Deps) any { return nil }\n",
		"helper.go":       "package jobs\n\ntype LonelyArgs struct{}\n\nfunc (*LonelyArgs) Kind() string { return \"lonely\" }\n",
		"archive_test.go": "package jobs\n\ntype FakeArgs struct{}\n\nfunc (FakeArgs) Kind() string { return \"fake\" }\n\nfunc Fake() {}\n",
		"register.go":     "package jobs\n",
		"register_gen.go": "package jobs\n\nthis is not Go\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := generator(t).Run(context.Background(), gen.Input{Root: root, Lock: lock("base", "api", "jobs"), Args: []string{"SendDigest"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range out.Plan.Files {
		got[f.Path+" "+f.Class] = string(f.Data)
	}
	jobFile, ok := got["api/internal/jobs/send_digest.go scaffold"]
	if !ok {
		t.Fatalf("plan = %v", keys(got))
	}
	for _, want := range []string{
		"type SendDigestArgs struct{}",
		`func (SendDigestArgs) Kind() string { return "send_digest" }`,
		"func SendDigest(d Deps) func(ctx context.Context, job jobs.Job[SendDigestArgs]) error {",
	} {
		if !strings.Contains(jobFile, want) {
			t.Errorf("send_digest.go does not contain %q:\n%s", want, jobFile)
		}
	}
	testFile := got["api/internal/jobs/send_digest_test.go scaffold"]
	if !strings.Contains(testFile, `"example.com/todo/api/db/migrations"`) || !strings.Contains(testFile, "func TestSendDigest(t *testing.T)") {
		t.Errorf("send_digest_test.go:\n%s", testFile)
	}
	register := got["api/internal/jobs/register_gen.go managed"]
	want := "\tjobs.Register(w, Archive(d))\n\tjobs.Register(w, SendDigest(d))\n}\n"
	if !strings.HasSuffix(register, want) || strings.Contains(register, "Lonely") || strings.Contains(register, "Fake") {
		t.Errorf("register_gen.go:\n%s", register)
	}
	if len(out.PostSteps) != 1 || out.PostSteps[0] != gen.PostGofmt {
		t.Errorf("post-steps = %v", out.PostSteps)
	}
	if len(out.Next) == 0 || len(out.Next) > 2 {
		t.Errorf("next = %v", out.Next)
	}

	// The same input gives the same plan.
	again, err := generator(t).Run(context.Background(), gen.Input{Root: root, Lock: lock("base", "api", "jobs"), Args: []string{"send_digest"}})
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range again.Plan.Files {
		if string(f.Data) != string(out.Plan.Files[i].Data) || f.Path != out.Plan.Files[i].Path {
			t.Fatalf("plan differs for %s", f.Path)
		}
	}
}

func TestScanSyntaxError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package jobs\nfunc {"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := job.Scan(dir, nil)
	if err == nil || !strings.Contains(err.Error(), "broken.go") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("err = %v", err)
	}
}

func TestScanMissingDir(t *testing.T) {
	t.Parallel()
	found, err := job.Scan(filepath.Join(t.TempDir(), "absent"), map[string][]byte{
		"ping.go": []byte("package jobs\ntype PingArgs struct{}\nfunc (PingArgs) Kind() string { return \"ping\" }\nfunc Ping(d Deps) any { return nil }\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0] != "Ping" {
		t.Fatalf("found = %v", found)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
