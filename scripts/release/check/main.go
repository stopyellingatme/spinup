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
	"github.com/stopyellingatme/spinup/internal/stackversions"
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
	if err := checkKitPin(root, version); err != nil {
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

// kitModule is the key of the kit pin in stackversions.yaml (ADR 0018 decision 1).
const kitModule = "github.com/stopyellingatme/spinup/kit"

// checkKitPin fails when the kit pin differs from the CLI candidate in major or minor version.
func checkKitPin(root, version string) error {
	data, err := os.ReadFile(filepath.Join(root, "internal", "stackversions", "stackversions.yaml"))
	if err != nil {
		return fmt.Errorf("cannot read the kit pin: %w\nTry: restore internal/stackversions/stackversions.yaml", err)
	}
	stack, err := stackversions.Parse(data)
	if err != nil {
		return fmt.Errorf("cannot read the kit pin: %w\nTry: fix internal/stackversions/stackversions.yaml", err)
	}
	pin, err := stack.GoModule(kitModule)
	if err != nil {
		return fmt.Errorf("cannot read the kit pin: %w\nTry: add %s to go_modules in internal/stackversions/stackversions.yaml", err, kitModule)
	}
	cli, err := compat.ParseVersion(version)
	if err != nil {
		return err
	}
	kit, err := compat.ParseVersion(pin)
	if err != nil {
		return fmt.Errorf("kit pin %q is not a version: %w\nTry: set the kit pin in stackversions.yaml to v%d.%d.0", pin, err, cli.Major, cli.Minor)
	}
	if cli.Major != kit.Major || cli.Minor != kit.Minor {
		return fmt.Errorf("kit pin %s in stackversions.yaml differs in major and minor version from CLI %s\nTry: push the tag kit/v%d.%d.0, then set the kit pin to v%d.%d.0 and update the pack go.sum", pin, version, cli.Major, cli.Minor, cli.Major, cli.Minor)
	}
	return nil
}
