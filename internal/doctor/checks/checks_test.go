package checks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestGitVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version string
		want    doctor.Status
	}{{"2.43.0", doctor.Fail}, {"2.44.0", doctor.OK}, {"2.50.1", doctor.OK}} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			fake := &exec.Fake{}
			fake.Expect("git", "--version").Return("git version "+tc.version+"\n", "", 0)
			got := gitVersion(context.Background(), &doctor.Context{Deps: map[string]any{"runner": fake}})
			if got.Status != tc.want {
				t.Fatalf("result=%+v", got)
			}
			fake.AssertDone(t)
		})
	}
}

func TestProjectChecks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lock := manifest.New()
	lock.Files["missing.yml"] = manifest.FileEntry{Class: manifest.Managed, Template: "base/missing.tmpl", Hash: manifest.Hash([]byte("data")), Mode: "0644"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	c := &doctor.Context{Root: root}
	if got := lockValid(context.Background(), c); got.Status != doctor.OK {
		t.Fatal(got)
	}
	if got := managedMissing(context.Background(), c); got.Status != doctor.Warn || got.Try != "spinup restore missing.yml" {
		t.Fatal(got)
	}
	lock.Tombstone("missing.yml")
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if got := managedMissing(context.Background(), c); got.Status != doctor.OK {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(root, "spinup.lock"), []byte("bad"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := lockValid(context.Background(), c); got.Status != doctor.Fail {
		t.Fatal(got)
	}
	for _, check := range doctor.Checks(doctor.Local, false) {
		if check.ID == "git-version" {
			continue
		}
		for _, c := range []*doctor.Context{{}, {Root: root, Quick: true}} {
			if got := check.Run(context.Background(), c); got.Status != doctor.Skip {
				t.Fatalf("%s: %+v", check.ID, got)
			}
		}
	}
}

func TestKitVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, cli, data string
		want            doctor.Status
	}{
		{"match", "0.2.1", `{"Require":[{"Path":"github.com/stopyellingatme/spinup/kit","Version":"v0.2.8"}]}`, doctor.OK},
		{"mismatch", "0.3.0", `{"Require":[{"Path":"github.com/stopyellingatme/spinup/kit","Version":"v0.2.8"}]}`, doctor.Warn},
		{"local", "0.3.0", `{"Replace":[{"Old":{"Path":"github.com/stopyellingatme/spinup/kit"},"New":{"Path":"../kit"}}]}`, doctor.Skip},
		{"missing", "0.2.0", `{}`, doctor.Warn},
		{"malformed", "0.2.0", `bad`, doctor.Warn},
		{"development", "0.0.0-dev", "", doctor.Skip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &exec.Fake{}
			if tc.data != "" {
				fake.Expect("go", "mod", "edit", "-json").Return(tc.data, "", 0)
			}
			got := kitVersion(context.Background(), &doctor.Context{Root: t.TempDir(), Deps: map[string]any{"runner": fake, "cli_version": tc.cli}})
			if got.Status != tc.want {
				t.Fatalf("result=%+v", got)
			}
			fake.AssertDone(t)
		})
	}
}
