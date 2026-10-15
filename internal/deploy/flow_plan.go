package deploy

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	textdiff "github.com/rogpeppe/go-internal/diff"
)

// readCurrentScript writes the current release directory as a tar stream. No current release writes nothing.
const readCurrentScript = `if [ -d "$1" ]; then tar -C "$1" -cf - .; fi`

// plan prints what a deploy would change. It only reads from the VM: no lock, no upload, no docker change.
func (f *flow) plan(ctx context.Context) error {
	if f.o.Tag != "" {
		if !sameSHA(f.o.Tag, f.l.sha) {
			return fmt.Errorf("--tag %q is not a release ID of HEAD %s\nTry: pass --tag <YYYYMMDDTHHMMSSZ>-<12-char HEAD SHA>", f.o.Tag, f.l.sha[:12])
		}
		f.release = f.o.Tag
	} else {
		id, err := ReleaseID(f.d.Now(), f.l.sha)
		if err != nil {
			return err
		}
		f.release = id
	}
	images, err := f.existingImages()
	if err != nil {
		return err
	}
	for i := range images {
		images[i].Ref = images[i].Tag
	}
	f.images = images
	if err := f.connect(ctx); err != nil {
		return err
	}
	defer func() { _ = f.vm.Close(context.WithoutCancel(ctx)) }()

	if f.o.Redeploy {
		if err := f.selectRelease(ctx); err != nil {
			return err
		}
	}

	upstream, err := ReadUpstream(ctx, f.vm, f.l.app)
	if err != nil {
		return err
	}
	live := LiveColor(upstream)
	f.color = OtherColor(live)
	cur, err := readRelease(ctx, f.vm, f.l.app, "")
	if err != nil {
		return err
	}
	result, err := f.cmd(ctx, false, "sh", "-c", readCurrentScript, "spinup-current", f.root()+"/current")
	if err != nil {
		return fmt.Errorf("read the current release: %w", err)
	}
	remoteFiles, err := untar(result.Stdout)
	if err != nil {
		return fmt.Errorf("read the current release: %w\nTry: inspect /opt/%s/current on the VM", err, f.l.app)
	}
	lock, err := ReadLock(ctx, f.vm, f.l.app)
	if err != nil {
		return err
	}

	w := f.out
	fmt.Fprintf(w, "Plan: deploy release %s to %s (%s)\n", f.release, f.l.env.Name, f.l.env.Host)
	if lock != nil {
		fmt.Fprintf(w, "Lock: held by %s for release %s. The deploy waits for it or fails with exit code 5.\n", lock.Owner, lock.Release)
	}
	if live == "" {
		fmt.Fprintf(w, "Color: none live -> %s (first deploy)\n", f.color)
	} else {
		fmt.Fprintf(w, "Color: %s -> %s\n", live, f.color)
	}
	fmt.Fprintln(w, "Images:")
	for _, img := range images {
		old := "none"
		if cur != nil {
			for _, c := range cur.Images {
				if c.Service == img.Service {
					old = c.Ref
				}
			}
		}
		fmt.Fprintf(w, "  %s: %s -> %s\n", img.Service, old, img.Tag)
	}

	deployed := ""
	if cur != nil {
		deployed = cur.MigrationVersion
	}
	pending := pendingMigrations(f.l.migrations, deployed)
	fmt.Fprintf(w, "Migrations: %d pending\n", len(pending))
	for _, m := range pending {
		fmt.Fprintf(w, "  %s\n", m)
	}

	f.planSecrets(w, cur)

	files, err := f.releaseFiles()
	if err != nil {
		return err
	}
	files = append(files, releaseFile{Path: "upstream.caddy", Data: UpstreamFile(f.color)})
	fmt.Fprintln(w, "Files (against current):")
	changed := 0
	names := map[string]bool{}
	for _, file := range files {
		names[file.Path] = true
		old, ok := remoteFiles[file.Path]
		switch {
		case !ok:
			fmt.Fprintf(w, "  added %s\n", file.Path)
			changed++
		case !bytes.Equal(old, file.Data):
			fmt.Fprintf(w, "  changed %s\n", file.Path)
			fmt.Fprintf(w, "%s", textdiff.Diff("current/"+file.Path, old, "new/"+file.Path, file.Data))
			changed++
		}
	}
	var removed []string
	for name := range remoteFiles {
		if !names[name] && name != "release.json" {
			removed = append(removed, name)
		}
	}
	slices.Sort(removed)
	for _, name := range removed {
		fmt.Fprintf(w, "  removed %s\n", name)
		changed++
	}
	if changed == 0 {
		fmt.Fprintln(w, "  no change")
	}
	fmt.Fprintln(w, "Nothing changed on the VM (--plan).")
	return nil
}

// planSecrets prints the names of the secrets that changed since the current release. It never prints a value.
func (f *flow) planSecrets(w io.Writer, cur *Release) {
	now := SecretHashes(f.l.secrets)
	var before map[string]string
	if cur != nil {
		before = cur.Secrets
	}
	var added, changed, removed []string
	for _, name := range secretNames(f.l.secrets) {
		old, ok := before[name]
		switch {
		case !ok:
			added = append(added, name)
		case old != now[name]:
			changed = append(changed, name)
		}
	}
	for name := range before {
		if _, ok := now[name]; !ok {
			removed = append(removed, name)
		}
	}
	slices.Sort(removed)
	if len(added)+len(changed)+len(removed) == 0 {
		fmt.Fprintln(w, "Secrets: no change")
		return
	}
	fmt.Fprintln(w, "Secrets:")
	for _, group := range []struct {
		label string
		names []string
	}{{"added", added}, {"changed", changed}, {"removed", removed}} {
		if len(group.names) > 0 {
			fmt.Fprintf(w, "  %s %s\n", group.label, strings.Join(group.names, ", "))
		}
	}
}

// untar reads the regular files of a tar stream. Paths are relative, with slashes.
func untar(data []byte) (map[string][]byte, error) {
	files := map[string][]byte{}
	if len(data) == 0 {
		return files, nil
	}
	r := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(r, 8<<20))
		if err != nil {
			return nil, err
		}
		files[strings.TrimPrefix(h.Name, "./")] = body
	}
}
