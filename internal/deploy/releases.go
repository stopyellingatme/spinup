package deploy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// ReleasesOptions selects the release history operation.
type ReleasesOptions struct {
	Options
	// Action is list, show, or prune. Empty selects list.
	Action string
	// ID selects the release for show.
	ID string
	// Keep is the number to retain for prune. Current and previous are always retained.
	Keep int
}

// Releases reads or prunes release history on the VM.
func Releases(ctx context.Context, o ReleasesOptions, d Deps) (err error) {
	o.fill()
	d.fill()
	if o.Action == "" {
		o.Action = "list"
	}
	if !slices.Contains([]string{"list", "show", "prune"}, o.Action) || (o.Action == "show" && !ValidReleaseID(o.ID)) || (o.Action == "prune" && o.Keep < 1) {
		return errors.New("invalid releases operation or arguments\nTry: use releases list, show <release-id>, or prune --keep <positive number>")
	}
	app, env, err := target(o.Options)
	if err != nil {
		return err
	}
	if o.Action == "prune" {
		if err := project.ConfirmProtected(env, "prune releases", o.Confirm); err != nil {
			return err
		}
	}
	vm, err := dial(ctx, o.Options, d, env)
	if err != nil {
		return err
	}
	defer func() { _ = vm.Close(context.WithoutCancel(ctx)) }()
	if o.Action == "show" {
		snap, err := readReleaseSnapshot(ctx, vm, app, o.ID)
		if err != nil {
			return err
		}
		// Snapshots contain hashes only. Reject invalid values before emitting any output.
		for _, hash := range snap.Secrets {
			b, err := hex.DecodeString(hash)
			if err != nil || len(b) != 32 {
				return errors.New("release snapshot contains an invalid secret hash\nTry: inspect release.json on the VM without publishing its contents")
			}
		}
		return writeReleaseJSON(o, snap)
	}
	if o.Action == "prune" {
		lock, lerr := AcquireLock(ctx, vm, app, LockOptions{Owner: d.User(), Release: "prune", TTL: o.LockTTL, Now: d.Now, Warn: o.ErrOut})
		if lerr != nil {
			return lerr
		}
		defer func() { err = errors.Join(err, lock.Release(context.WithoutCancel(ctx))) }()
	}
	current, err := readRelease(ctx, vm, app, "")
	if err != nil {
		return err
	}
	entries, err := ReadReleases(ctx, vm, app)
	if err != nil {
		return err
	}
	currentID := ""
	if current != nil {
		currentID = current.Release
	}
	if o.Action == "prune" {
		return pruneReleases(ctx, vm, app, currentID, entries, o)
	}
	if o.JSON {
		return writeReleaseJSON(o, struct {
			Current  string         `json:"current"`
			Releases []ReleaseEntry `json:"releases"`
		}{currentID, entries})
	}
	fmt.Fprintln(o.Out, "CURRENT ID GIT_SHA TIME USER MIGRATION")
	for _, entry := range entries {
		mark := "-"
		if entry.Release == currentID {
			mark = "*"
		}
		fmt.Fprintf(o.Out, "%s %s %s %s %s %s\n", mark, entry.Release, short(entry.GitSHA), entry.CreatedAt.UTC().Format(time.RFC3339), entry.User, entry.MigrationVersion)
	}
	return nil
}

func writeReleaseJSON(o ReleasesOptions, value any) error {
	enc := json.NewEncoder(o.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func pruneEntries(entries []ReleaseEntry, current string, keep int) (kept, removed []ReleaseEntry) {
	protected := map[string]bool{current: true}
	for i, e := range entries {
		if e.Release == current && i+1 < len(entries) {
			protected[entries[i+1].Release] = true
		}
	}
	for i, e := range entries {
		if i < keep || protected[e.Release] {
			kept = append(kept, e)
		} else {
			removed = append(removed, e)
		}
	}
	return kept, removed
}

func pruneReleases(ctx context.Context, vm remote.Runner, app, current string, entries []ReleaseEntry, o ReleasesOptions) error {
	if !ValidReleaseID(current) || !slices.ContainsFunc(entries, func(e ReleaseEntry) bool { return e.Release == current }) {
		return errors.New("cannot prune releases: current is missing from the history\nTry: inspect current and releases.json on the VM")
	}
	kept, removed := pruneEntries(entries, current, o.Keep)
	for _, e := range entries {
		if !ValidReleaseID(e.Release) {
			return errors.New("cannot prune releases: history contains an invalid release ID\nTry: inspect releases.json on the VM")
		}
	}
	root := "/opt/" + app
	// Update history first. Interrupted cleanup leaves harmless unlisted directories.
	data, _ := json.MarshalIndent(kept, "", "  ")
	if err := vm.Upload(ctx, append(data, '\n'), root+"/releases.json", 0644); err != nil {
		return fmt.Errorf("write release history: %w\nTry: check the SSH connection", err)
	}
	for _, e := range removed {
		if _, err := vm.Run(ctx, remote.Cmd{Argv: []string{"rm", "-rf", "--", root + "/releases/" + e.Release}}); err != nil {
			return fmt.Errorf("prune release %s: %w\nTry: check the SSH connection", e.Release, err)
		}
	}
	fmt.Fprintf(o.Out, "Kept %d releases, removed %d\n", len(kept), len(removed))
	return nil
}
