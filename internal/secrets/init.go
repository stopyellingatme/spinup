package secrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/codemod"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"go.yaml.in/yaml/v3"
)

// Init creates an environment key, its creation rule, and an encrypted empty map.
// Recovery is required unless NoRecovery explicitly accepts the loss of recovery.
func Init(ctx context.Context, o Options, r runexec.Runner) error {
	e, err := resolve(o)
	if err != nil {
		return err
	}
	if o.RecoveryRecipient == "" && !o.NoRecovery {
		return problem("initialize secrets", "a recovery recipient is required", "pass --recovery-recipient age1... or explicitly pass --no-recovery")
	}
	if o.NoRecovery && o.RecoveryRecipient != "" {
		return problem("initialize secrets", "recovery options conflict", "use --recovery-recipient or --no-recovery")
	}
	for _, recipient := range []string{o.RecoveryRecipient, o.CIRecipient} {
		if recipient != "" && !recipientPattern.MatchString(recipient) {
			return problem("initialize secrets", "an age recipient is invalid", "pass an age1 recipient from age-keygen")
		}
	}
	if o.NoRecovery {
		if o.ErrOut == nil {
			return problem("initialize secrets", "the recovery warning cannot be displayed", "provide stderr")
		}
		if _, err = io.WriteString(o.ErrOut, "WARNING: No recovery recipient. Losing the environment key can permanently destroy access to these secrets.\n"); err != nil {
			return problem("initialize secrets", "the recovery warning could not be written", "check stderr")
		}
	}
	keyInfo, keyErr := os.Lstat(e.key)
	keyExists := keyErr == nil
	if keyErr != nil && !errors.Is(keyErr, os.ErrNotExist) || keyExists && !keyInfo.Mode().IsRegular() {
		return problem("initialize secrets", "the existing key cannot be used", "check the environment key path")
	}
	target := filepath.Join(e.root, e.path)
	if err = absent(target); err != nil {
		return err
	}
	configPath := filepath.Join(e.root, ".sops.yaml")
	source, err := os.ReadFile(configPath)
	configExists := err == nil
	if errors.Is(err, os.ErrNotExist) {
		source = []byte("# Encrypt secrets for each environment.\ncreation_rules: []\n")
	} else if err != nil {
		return problem("initialize secrets", ".sops.yaml could not be read", "check .sops.yaml permissions")
	}
	// Validate the configuration before generating any key material.
	if _, err = creationRule(source, e.name, []string{"age1placeholder"}); err != nil {
		return err
	}
	if o.DryRun {
		return nil
	}
	if err = privateDir(filepath.Dir(e.key)); err != nil {
		return err
	}
	if !keyExists {
		if err = runTool(ctx, r, runexec.Cmd{Name: "age-keygen", Args: []string{"-o", e.key}, Dir: e.root}); err != nil {
			return err
		}
	}
	// Keep the key if any later step fails. Never destroy generated key material.
	if err = os.Chmod(e.key, 0600); err != nil {
		return problem("secure environment key", "key permissions could not be changed", "set the environment key mode to 0600")
	}
	result, err := r.Run(ctx, runexec.Cmd{Name: "age-keygen", Args: []string{"-y", e.key}, Dir: e.root})
	if err != nil {
		return problem("read environment recipient", "age-keygen could not derive the recipient", "check the environment key")
	}
	recipient := strings.TrimSpace(string(result.Stdout))
	if !recipientPattern.MatchString(recipient) {
		return problem("read environment recipient", "age-keygen returned an invalid recipient", "check the installed age-keygen binary")
	}
	recipients := []string{recipient}
	for _, extra := range []string{o.RecoveryRecipient, o.CIRecipient} {
		if extra != "" {
			recipients = append(recipients, extra)
		}
	}
	config, err := creationRule(source, e.name, recipients)
	if err != nil {
		return err
	}
	// Sops requires a matching rule even when --age supplies recipients.
	if err = atomicWrite(configPath, config, 0644); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			if configExists {
				_ = atomicWrite(configPath, source, 0644)
			} else {
				_ = os.Remove(configPath)
			}
		}
	}()
	c := e.command("encrypt", "--age", strings.Join(recipients, ","), "--filename-override", filepath.ToSlash(e.path), "--input-type", "json", "--output-type", "yaml")
	c.Stdin = bytes.NewBufferString("{}")
	result, err = r.Run(ctx, c)
	if err != nil {
		return problem("encrypt initial secrets", "sops could not encrypt the empty map", "check the recipients and retry with the existing key")
	}
	if !bytes.Contains(result.Stdout, []byte("sops:")) {
		return problem("encrypt initial secrets", "sops returned no encryption metadata", "check the installed sops binary")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return problem("create secrets directory", "the directory could not be created", "check project permissions")
	}
	if err = atomicWrite(target, result.Stdout, 0600); err != nil {
		return err
	}
	complete = true
	return nil
}

func creationRule(source []byte, env string, recipients []string) ([]byte, error) {
	data, _, err := codemod.EditYAML(source, func(doc *yaml.Node) (bool, error) {
		rules := codemod.YAMLGet(doc, "creation_rules")
		if rules == nil {
			if err := codemod.YAMLSet(doc, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}, "creation_rules"); err != nil {
				return false, err
			}
			rules = codemod.YAMLGet(doc, "creation_rules")
		}
		if rules.Kind != yaml.SequenceNode {
			return false, errors.New("creation_rules must be a sequence")
		}
		pattern := `^secrets/` + env + `\.enc\.yaml$`
		var rule *yaml.Node
		for _, entry := range rules.Content {
			path := codemod.YAMLGet(entry, "path_regex")
			if path != nil && path.Value == pattern {
				if rule != nil {
					return false, errors.New("duplicate environment rules")
				}
				rule = entry
			}
		}
		if rule == nil {
			rule = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if err := codemod.YAMLSet(rule, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: pattern}, "path_regex"); err != nil {
				return false, err
			}
			rules.Content = append(rules.Content, rule)
		}
		if rules.LineComment != "" {
			rules.HeadComment = strings.TrimSpace(rules.HeadComment + "\n" + rules.LineComment)
			rules.LineComment = ""
		}
		rules.Style = 0
		return true, codemod.YAMLSet(rule, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.Join(recipients, ",")}, "age")
	})
	if err != nil {
		return nil, problem("edit .sops.yaml", "creation rules are invalid", "correct creation_rules in .sops.yaml")
	}
	return data, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".spinup-secrets-*")
	if err != nil {
		return problem("write secrets file", "a temporary file could not be created", "check project permissions")
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(data)
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return problem("write secrets file", "the file could not be replaced", "check project permissions")
	}
	return nil
}
