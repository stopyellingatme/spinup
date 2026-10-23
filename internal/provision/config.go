package provision

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/stopyellingatme/spinup/internal/codemod"
	"github.com/stopyellingatme/spinup/internal/project"
)

// stored is the [env.<name>.digitalocean] table of spinup.toml (ADR 0017 §5).
type stored struct {
	DropletID  int64  `toml:"droplet_id"`
	Region     string `toml:"region"`
	Size       string `toml:"size"`
	Image      string `toml:"image"`
	Tag        string `toml:"tag"`
	FirewallID string `toml:"firewall_id"`
	Team       string `toml:"team"`
	Domain     string `toml:"domain"`
}

func readStored(root, env string) (stored, error) {
	data, err := os.ReadFile(filepath.Join(root, project.ConfigFile))
	if err != nil {
		return stored{}, failWrap("read spinup.toml", err, "check the file permissions")
	}
	var doc struct {
		Env map[string]struct {
			DigitalOcean stored `toml:"digitalocean"`
		} `toml:"env"`
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return stored{}, failWrap("parse the digitalocean table of spinup.toml", err, "correct the TOML syntax")
	}
	return doc.Env[env].DigitalOcean, nil
}

// edit sets or deletes one key of spinup.toml.
type edit struct {
	table []string
	key   string
	value any
	del   bool
}

func set(table []string, key string, value any) edit {
	return edit{table: table, key: key, value: value}
}

func del(table []string, key string) edit { return edit{table: table, key: key, del: true} }

// editConfig applies the edits to spinup.toml. It keeps comments and replaces the file atomically.
func editConfig(root string, edits []edit) error {
	path := filepath.Join(root, project.ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return failWrap("read spinup.toml", err, "check the file permissions")
	}
	out := data
	for _, e := range edits {
		if e.del {
			out, _, err = codemod.DeleteTOML(out, e.table, e.key)
		} else {
			out, err = codemod.SetTOML(out, e.table, e.key, e.value)
		}
		if err != nil {
			return failWrap("update spinup.toml", fmt.Errorf("key %s: %w", e.key, err), "check spinup.toml, then run the command again")
		}
	}
	if bytes.Equal(out, data) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return failWrap("inspect spinup.toml", err, "check the file permissions")
	}
	file, err := os.CreateTemp(root, ".spinup-provision-*")
	if err != nil {
		return failWrap("create the new spinup.toml", err, "check the directory permissions")
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(out); err != nil {
		_ = file.Close()
		return failWrap("write the new spinup.toml", err, "check the free disk space")
	}
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		_ = file.Close()
		return failWrap("set the mode of spinup.toml", err, "check the directory permissions")
	}
	if err := file.Close(); err != nil {
		return failWrap("close the new spinup.toml", err, "check the free disk space")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return failWrap("save spinup.toml", err, "check the directory permissions")
	}
	return nil
}

// pinHostKeys writes host_keys and updates the in-memory environment.
func (b *base) pinHostKeys(keys []string) error {
	if err := editConfig(b.root, []edit{set([]string{"env", b.env}, "host_keys", keys)}); err != nil {
		return err
	}
	b.envCfg.HostKeys = append([]string(nil), keys...)
	return nil
}

// normalizeKey reduces a public key line to "<type> <blob>".
func normalizeKey(line string) (string, error) {
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "ssh-ed25519" {
		return "", errors.New("the key is not an ssh-ed25519 public key")
	}
	return f[0] + " " + f[1], nil
}
