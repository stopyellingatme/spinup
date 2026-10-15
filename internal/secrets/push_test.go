package secrets

import (
	"context"
	"io/fs"
	"reflect"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

type upload struct {
	path, data string
	mode       fs.FileMode
}
type fakeUploader struct {
	runexec.Fake
	files []upload
}

func (f *fakeUploader) Upload(_ context.Context, data []byte, path string, mode fs.FileMode) error {
	f.files = append(f.files, upload{path, string(data), mode})
	return nil
}

func TestPush(t *testing.T) {
	t.Parallel()
	f := &fakeUploader{}
	f.Expect("sh").Return("", "", 0)
	f.Expect("find").Return("/opt/demo/secrets/OLD\n/opt/demo/secrets/KEEP\n", "", 0)
	f.Expect("rm", "-f", "--", "/opt/demo/secrets/OLD").Return("", "", 0)
	err := Push(context.Background(), f, "demo", map[string]string{"NEW": "secret-new", "KEEP": "secret-keep"})
	if err != nil {
		t.Fatal(err)
	}
	want := []upload{{"/opt/demo/secrets/KEEP", "secret-keep", 0600}, {"/opt/demo/secrets/NEW", "secret-new", 0600}}
	if !reflect.DeepEqual(f.files, want) {
		t.Fatalf("uploads=%v", f.files)
	}
	f.AssertDone(t)
	for _, call := range f.Calls() {
		for _, arg := range call.Args {
			if arg == "secret-new" || arg == "secret-keep" {
				t.Fatal("value in argv")
			}
		}
	}
}

func TestPushRejectsNamesBeforeMutation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ app, name string }{{"../demo", "NAME"}, {"demo", "../NAME"}, {"demo", "A;evil"}} {
		f := &fakeUploader{}
		if err := Push(context.Background(), f, tt.app, map[string]string{tt.name: "secret"}); err == nil || len(f.Calls()) != 0 {
			t.Fatalf("validation=%v", err)
		}
	}
}
