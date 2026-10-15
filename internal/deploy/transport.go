package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// Image transports of ADR 0009 §4.
const (
	TransportSSH      = "ssh"
	TransportRegistry = "registry"
)

// Image is the deploy image of one service.
type Image struct {
	// Service is api or web.
	Service string `json:"service"`
	// Tag is the tagged reference that the build created, for example app-api:<release-id>.
	Tag string `json:"tag"`
	// Ref is the reference that Compose runs on the VM: the tag (ssh) or repository@digest (registry).
	Ref string `json:"ref"`
	// Digest is the registry manifest digest (registry transport).
	Digest string `json:"digest,omitempty"`
	// ID is the local image ID (ssh transport).
	ID string `json:"id,omitempty"`
}

// transporter moves images from the local Docker engine to the VM.
type transporter struct {
	exec   runexec.Runner
	vm     remote.Runner
	redact []string
	out    io.Writer
}

// transport runs the transport of the environment and returns the images with their deploy references.
func (t transporter) transport(ctx context.Context, kind string, images []Image) ([]Image, error) {
	switch kind {
	case "", TransportSSH:
		return t.ssh(ctx, images)
	case TransportRegistry:
		return t.registry(ctx, images)
	}
	return nil, fmt.Errorf("unknown image transport %q\nTry: set transport to ssh or registry in spinup.toml", kind)
}

func tags(images []Image) []string {
	out := make([]string, len(images))
	for i, img := range images {
		out[i] = img.Tag
	}
	return out
}

// imageFingerprint identifies image content across Docker image stores: the platform and the
// uncompressed layer digests. The image ID is not portable. The classic store uses the config
// digest, and the containerd store uses the manifest digest.
const imageFingerprint = "{{.Os}}/{{.Architecture}} {{json .RootFS.Layers}}"

// localImages returns the local image IDs and fingerprints of refs, in order.
func localImages(ctx context.Context, r runexec.Runner, refs []string) (ids, prints []string, err error) {
	result, err := r.Run(ctx, runexec.Cmd{Name: "docker", Args: append([]string{"image", "inspect", "--format", "{{.Id}} " + imageFingerprint}, refs...)})
	if err != nil {
		return nil, nil, fmt.Errorf("find the local images %s: %w\nTry: build them with spinup deploy (without --skip-build)", strings.Join(refs, ", "), err)
	}
	for _, line := range lines(result.Stdout) {
		id, print, _ := strings.Cut(line, " ")
		ids, prints = append(ids, id), append(prints, print)
	}
	if len(ids) != len(refs) {
		return nil, nil, fmt.Errorf("find the local images %s: docker returned %d images\nTry: build the images again", strings.Join(refs, ", "), len(ids))
	}
	return ids, prints, nil
}

func lines(data []byte) []string {
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// remotePrints returns the image fingerprints on the VM, or nil when an image is missing there.
func (t transporter) remotePrints(ctx context.Context, refs []string) ([]string, error) {
	argv := append([]string{"docker", "image", "inspect", "--format", imageFingerprint}, refs...)
	result, err := t.vm.Run(ctx, remote.Cmd{Argv: argv, Redact: t.redact})
	if err != nil {
		if result.ExitCode == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect the images on the VM: %w\nTry: check the SSH connection and Docker on the VM", err)
	}
	return lines(result.Stdout), nil
}

// ssh streams docker save into docker load on the VM. It skips images that the VM already has.
func (t transporter) ssh(ctx context.Context, images []Image) ([]Image, error) {
	refs := tags(images)
	ids, prints, err := localImages(ctx, t.exec, refs)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(images)
	for i := range out {
		out[i].Ref, out[i].ID = out[i].Tag, ids[i]
	}
	have, err := t.remotePrints(ctx, refs)
	if err != nil {
		return nil, err
	}
	if slices.Equal(have, prints) {
		fmt.Fprintf(t.out, "The VM already has %s\n", strings.Join(refs, ", "))
		return out, nil
	}
	dir, err := os.MkdirTemp("", "spinup-images-*")
	if err != nil {
		return nil, fmt.Errorf("create a temporary directory for the images: %w\nTry: check the temporary directory", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	archive := filepath.Join(dir, "images.tar")
	fmt.Fprintf(t.out, "Saving %s\n", strings.Join(refs, ", "))
	if _, err := t.exec.Run(ctx, runexec.Cmd{Name: "docker", Args: append([]string{"save", "-o", archive}, refs...)}); err != nil {
		return nil, fmt.Errorf("save the images: %w\nTry: check the local Docker engine and disk space", err)
	}
	f, err := os.Open(archive)
	if err != nil {
		return nil, fmt.Errorf("open the image archive: %w\nTry: check the temporary directory", err)
	}
	defer f.Close()
	fmt.Fprintln(t.out, "Loading the images on the VM")
	if _, err := t.vm.Run(ctx, remote.Cmd{Argv: []string{"docker", "load"}, Stdin: f, Stdout: t.out, Stderr: t.out, Redact: t.redact}); err != nil {
		return nil, fmt.Errorf("load the images on the VM: %w\nTry: check the SSH connection and the disk space of the VM", err)
	}
	have, err = t.remotePrints(ctx, refs)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(have, prints) {
		return nil, fmt.Errorf("the images on the VM do not match the local images after docker load\nTry: run spinup deploy --resume")
	}
	return out, nil
}

// registry pulls each image by digest on the VM. The build pushed them.
func (t transporter) registry(ctx context.Context, images []Image) ([]Image, error) {
	out := slices.Clone(images)
	for i := range out {
		if out[i].Digest == "" {
			digest, err := t.manifestDigest(ctx, out[i].Tag)
			if err != nil {
				return nil, err
			}
			out[i].Digest = digest
		}
		if !strings.HasPrefix(out[i].Digest, "sha256:") {
			return nil, fmt.Errorf("image %s has the invalid digest %q\nTry: push the image again with spinup build --image --push", out[i].Tag, out[i].Digest)
		}
		out[i].Ref = repository(out[i].Tag) + "@" + out[i].Digest
		fmt.Fprintf(t.out, "Pulling %s on the VM\n", out[i].Ref)
		if _, err := t.vm.Run(ctx, remote.Cmd{Argv: []string{"docker", "pull", "--quiet", out[i].Ref}, Stdout: t.out, Stderr: t.out, Redact: t.redact}); err != nil {
			return nil, fmt.Errorf("pull %s on the VM: %w\nTry: log in to the registry on the VM with docker login, and run spinup deploy --resume", out[i].Ref, err)
		}
	}
	return out, nil
}

// manifestDigest reads the digest of a pushed image from the registry.
func (t transporter) manifestDigest(ctx context.Context, ref string) (string, error) {
	var manifest struct {
		Digest string `json:"digest"`
	}
	c := runexec.Cmd{Name: "docker", Args: []string{"buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", ref}}
	if err := runexec.RunJSON(ctx, t.exec, c, &manifest); err != nil {
		return "", fmt.Errorf("read the digest of %s: %w\nTry: push the image with spinup build --image --push, or deploy without --skip-build", ref, err)
	}
	if manifest.Digest == "" {
		return "", errors.New("read the digest of " + ref + ": the registry returned no digest\nTry: push the image again")
	}
	return manifest.Digest, nil
}

// repository removes the tag from an image reference. A registry port is not a tag.
func repository(ref string) string {
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon]
	}
	return ref
}

// imageDetail encodes the images for the journal. It holds no secret.
func imageDetail(images []Image) string {
	data, _ := json.Marshal(images)
	return string(data)
}
