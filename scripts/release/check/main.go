// Command check validates the release version, tags, changelog, tree, and trust keys.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/templates/store"
)

func main() {
	// The version precedes flags in the public shell interface.
	if len(os.Args) < 2 {
		fail(errors.New("version is missing"))
	}
	version := strings.TrimPrefix(os.Args[1], "v")
	flags := flag.NewFlagSet("release check", flag.ExitOnError)
	snapshot := flags.Bool("snapshot", false, "skip template trust checks")
	tagged := flags.Bool("tagged", false, "require the CLI tag at HEAD instead of an absent tag")
	kitVersion := flags.String("kit-version", version, "prospective kit version (defaults to the CLI version)")
	if err := flags.Parse(os.Args[2:]); err != nil {
		fail(err)
	}
	if err := check(".", version, *kitVersion, *snapshot, *tagged); err != nil {
		fail(err)
	}
	fmt.Println("Release checks passed for v" + version)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "cannot release spinup: %v\nTry: resolve the release checks, then run scripts/release/check.sh <version> again\n", err)
	os.Exit(1)
}

func git(root string, args ...string) (string, error) {
	result, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "git", Args: args, Dir: root})
	return strings.TrimSpace(string(result.Stdout)), err
}

func check(root, version, kitVersion string, snapshot, tagged bool) error {
	if _, err := compat.ParseVersion(version); err != nil {
		return err
	}
	if _, err := compat.ParseVersion(kitVersion); err != nil {
		return err
	}
	status, err := git(root, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("working tree is not clean")
	}
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		return err
	}
	heading := regexp.MustCompile(`(?m)^## \[` + regexp.QuoteMeta(version) + `\](?:\s|$)`)
	if !heading.Match(changelog) {
		return fmt.Errorf("CHANGELOG.md has no section for %s", version)
	}
	tags, err := git(root, "tag", "--list")
	if err != nil {
		return err
	}
	exists := func(tag string) bool {
		for _, value := range strings.Fields(tags) {
			if value == tag {
				return true
			}
		}
		return false
	}
	cliTag, kitTag := "v"+version, "kit/v"+strings.TrimPrefix(kitVersion, "v")
	head, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if tagged {
		if !exists(cliTag) {
			return fmt.Errorf("tag %s is missing", cliTag)
		}
		target, err := git(root, "rev-parse", cliTag+"^{commit}")
		if err != nil {
			return err
		}
		if target != head {
			return fmt.Errorf("tag %s does not identify HEAD", cliTag)
		}
	} else if exists(cliTag) {
		return fmt.Errorf("tag %s already exists", cliTag)
	}
	if exists(kitTag) {
		target, err := git(root, "rev-parse", kitTag+"^{commit}")
		if err != nil {
			return err
		}
		if !tagged || target != head {
			return fmt.Errorf("tag %s already exists at an incompatible commit", kitTag)
		}
	}
	// Compare the prospective tags. The workflow creates both on this commit.
	// A kit change requires matching minors. Independent patches are permitted.
	changed := true
	previous, err := git(root, "describe", "--tags", "--match", "kit/v*", "--abbrev=0", "HEAD")
	if err == nil {
		changes, err := git(root, "diff", "--name-only", previous, "HEAD", "--", "kit")
		if err != nil {
			return err
		}
		changed = changes != ""
	}
	if changed {
		if err := compat.CheckKit(version, kitVersion); err != nil {
			return err
		}
	}
	if !snapshot {
		if len(store.TrustedKeys) == 0 {
			return errors.New("store.TrustedKeys is empty")
		}
		decoded, err := base64.StdEncoding.DecodeString(os.Getenv("SPINUP_TEMPLATES_SIGNING_KEY"))
		if err != nil || len(decoded) != ed25519.PrivateKeySize {
			return errors.New("SPINUP_TEMPLATES_SIGNING_KEY must contain a base64 ed25519 private key")
		}
		key := ed25519.PrivateKey(decoded)
		matched := false
		for _, public := range store.TrustedKeys {
			if public.Equal(key.Public()) {
				matched = true
			}
		}
		if !matched {
			return errors.New("template signing key does not match store.TrustedKeys")
		}
	}
	return nil
}
