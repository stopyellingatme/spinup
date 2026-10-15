package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestTransportSSHSkipsPresentImages(t *testing.T) {
	t.Parallel()
	local, vm := &runexec.Fake{}, &remote.Fake{}
	refs := []string{"todo-api:r", "todo-web:r"}
	local.Expect("docker", append([]string{"image", "inspect", "--format", "{{.Id}}"}, refs...)...).Return("sha256:a\nsha256:w\n", "", 0)
	vm.Expect(append([]string{"docker", "image", "inspect", "--format", "{{.Id}}"}, refs...)...).Return("sha256:a\nsha256:w\n", "", 0)
	var out bytes.Buffer
	images, err := transporter{exec: local, vm: vm, out: &out}.transport(context.Background(), TransportSSH, []Image{{Service: "api", Tag: refs[0]}, {Service: "web", Tag: refs[1]}})
	if err != nil {
		t.Fatal(err)
	}
	local.AssertDone(t)
	vm.AssertDone(t)
	if images[1].Ref != "todo-web:r" || images[1].ID != "sha256:w" || !strings.Contains(out.String(), "already has") {
		t.Fatalf("images = %#v, out = %s", images, out.String())
	}
}

func TestTransportSSHFailures(t *testing.T) {
	t.Parallel()
	t.Run("missing local image", func(t *testing.T) {
		t.Parallel()
		local, vm := &runexec.Fake{}, &remote.Fake{}
		local.Expect("docker", "image", "inspect").Return("", "No such image", 1)
		_, err := transporter{exec: local, vm: vm, out: &bytes.Buffer{}}.ssh(context.Background(), []Image{{Service: "api", Tag: "todo-api:r"}})
		if err == nil || !strings.Contains(err.Error(), "without --skip-build") || len(vm.Calls()) != 0 {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("SSH failure", func(t *testing.T) {
		t.Parallel()
		local, vm := &runexec.Fake{}, &remote.Fake{}
		local.Expect("docker", "image", "inspect").Return("sha256:a\n", "", 0)
		vm.Expect("docker", "image", "inspect").Return("", "Connection closed", 255)
		_, err := transporter{exec: local, vm: vm, out: &bytes.Buffer{}}.ssh(context.Background(), []Image{{Service: "api", Tag: "todo-api:r"}})
		if err == nil || !strings.Contains(err.Error(), "SSH connection") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("load mismatch", func(t *testing.T) {
		t.Parallel()
		local, vm := &runexec.Fake{}, &remote.Fake{}
		local.Expect("docker", "image", "inspect").Return("sha256:a\n", "", 0)
		vm.Expect("docker", "image", "inspect").Return("sha256:old\n", "", 0)
		local.Expect("docker", "save").Return("", "", 0)
		vm.Expect("docker", "load").Return("", "", 0)
		vm.Expect("docker", "image", "inspect").Return("sha256:old\n", "", 0)
		_, err := transporter{exec: savingExec{local}, vm: vm, out: &bytes.Buffer{}}.ssh(context.Background(), []Image{{Service: "api", Tag: "todo-api:r"}})
		if err == nil || !strings.Contains(err.Error(), "do not match") {
			t.Fatalf("error = %v", err)
		}
		if c := vm.Calls()[1]; c.Stdin == nil {
			t.Fatal("docker load received no archive")
		}
	})
}

func TestTransportRegistryResolvesDigest(t *testing.T) {
	t.Parallel()
	local, vm := &runexec.Fake{}, &remote.Fake{}
	local.Expect("docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", "ghcr.io/acme/todo-api:r").Return(`{"mediaType":"x","digest":"`+digestAPI+`","size":1}`, "", 0)
	vm.Expect("docker", "pull", "--quiet", "ghcr.io/acme/todo-api@"+digestAPI).Return("", "", 0)
	images, err := transporter{exec: local, vm: vm, out: &bytes.Buffer{}}.transport(context.Background(), TransportRegistry, []Image{{Service: "api", Tag: "ghcr.io/acme/todo-api:r"}})
	if err != nil || images[0].Digest != digestAPI || images[0].Ref != "ghcr.io/acme/todo-api@"+digestAPI {
		t.Fatalf("images = %#v, %v", images, err)
	}
	local.AssertDone(t)
	vm.AssertDone(t)
	if _, err := (transporter{exec: local, vm: vm, out: &bytes.Buffer{}}).transport(context.Background(), TransportRegistry, []Image{{Tag: "x:r", Digest: "md5:1"}}); err == nil {
		t.Fatal("invalid digest accepted")
	}
	if _, err := (transporter{}).transport(context.Background(), "ftp", nil); err == nil {
		t.Fatal("unknown transport accepted")
	}
}
