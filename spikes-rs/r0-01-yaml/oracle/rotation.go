// Oracle copy of internal/provision/userdata_rotate.go.
package main

import (
	"errors"
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"
)

var rotateAppName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// hostKeyScript is the spinup-host-key tool. The environment variables SPINUP_HOSTKEY_ETC,
// SPINUP_HOSTKEY_STATE, and SPINUP_HOSTKEY_RELOAD exist for tests. The defaults are the real paths.
const hostKeyScript = `#!/bin/sh
# Rotates the SSH host key of this VM in two phases. Commands: next, activate, status.
set -eu
etc="${SPINUP_HOSTKEY_ETC:-/etc/ssh}"
state="${SPINUP_HOSTKEY_STATE:-/var/lib/spinup}"
key="$etc/ssh_host_ed25519_key"
marker="$state/host-key-rotated"

# print_key prints the public key as one line without the comment.
print_key() {
  awk '{ print $1 " " $2; exit }' "$1"
}

# lock serializes next and activate, so two spinup runs cannot race on the key files.
# A lock older than two minutes is left by a run that died, so the tool removes it.
lock() {
  dir="$state/host-key.lock"
  mkdir -p "$state"
  tries=0
  until mkdir "$dir" 2>/dev/null; do
    if [ -n "$(find "$dir" -maxdepth 0 -mmin +2 2>/dev/null)" ]; then
      rmdir "$dir" 2>/dev/null || true
      continue
    fi
    tries=$((tries + 1))
    if [ "$tries" -ge 60 ]; then
      echo "spinup-host-key: another run holds $dir" >&2
      exit 1
    fi
    sleep 1
  done
  trap 'rmdir "$dir" 2>/dev/null || true' EXIT
}

reload_sshd() {
  if [ -n "${SPINUP_HOSTKEY_RELOAD:-}" ]; then
    "$SPINUP_HOSTKEY_RELOAD"
  else
    systemctl reload ssh.service || systemctl restart ssh.service
  fi
}

case "${1:-}" in
  next)
    lock
    if [ ! -s "$key.next" ] || [ ! -s "$key.next.pub" ]; then
      # Remove a partial pair, also a dangling link, so ssh-keygen never asks to overwrite.
      rm -f "$key.next" "$key.next.pub"
      ssh-keygen -q -t ed25519 -N '' -C '' -f "$key.next" </dev/null
    fi
    print_key "$key.next.pub"
    ;;
  activate)
    lock
    if [ -e "$key.next" ]; then
      chmod 0600 "$key.next"
      chmod 0644 "$key.next.pub"
      mv -f "$key.next" "$key"
      mv -f "$key.next.pub" "$key.pub"
      reload_sshd
      mkdir -p "$state"
      touch "$marker"
    fi
    print_key "$key.pub"
    ;;
  status)
    if [ -e "$marker" ]; then
      echo rotated
    else
      echo pending
    fi
    ;;
  *)
    echo "usage: spinup-host-key next|activate|status" >&2
    exit 2
    ;;
esac
`

// addRotationTooling adds the spinup-host-key script and its sudoers rule to a parsed cloud-init
// document (ADR 0017 §6). The call is idempotent.
func addRotationTooling(doc *yaml.Node, app string) error {
	if !rotateAppName.MatchString(app) {
		return fmt.Errorf("host key rotation: the app name %q is not valid\nTry: use a name that matches ^[a-z][a-z0-9-]{0,62}$", app)
	}
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("host key rotation: the user data is not a YAML document with a mapping\nTry: check the cloud-init template output")
	}
	root := doc.Content[0]
	files, err := rotateSeq(root, "write_files")
	if err != nil {
		return err
	}
	cmds, err := rotateSeq(root, "runcmd")
	if err != nil {
		return err
	}

	staged := "/run/spinup-" + app + "-hostkey-sudoers"
	rule := "deploy ALL=(root) NOPASSWD: " + hostKeyTool + " next, " + hostKeyTool + " activate, " + hostKeyTool + " status\n"
	if !rotateHasPath(files, hostKeyTool) {
		files.Content = append(files.Content,
			rotateFile(hostKeyTool, "0755", hostKeyScript),
			rotateFile(staged, "0440", rule))
	}
	cmd := "visudo -cf " + staged + " && mv -f " + staged + " /etc/sudoers.d/spinup-" + app + "-hostkey"
	for _, c := range cmds.Content {
		if c.Kind == yaml.SequenceNode && len(c.Content) == 3 && c.Content[2].Value == cmd {
			return nil
		}
	}
	entry := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "sh"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "-ec"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: cmd, Style: yaml.DoubleQuotedStyle},
	}}
	cmds.Content = append(cmds.Content, entry)
	return nil
}

// rotateSeq returns the sequence under key. It creates the sequence when the key is missing.
func rotateSeq(root *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			v := root.Content[i+1]
			if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
				v.Kind, v.Tag, v.Value, v.Style = yaml.SequenceNode, "!!seq", "", 0
			}
			if v.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("host key rotation: %s in the user data is not a list\nTry: check the cloud-init template output", key)
			}
			return v, nil
		}
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, seq)
	return seq, nil
}

func rotateHasPath(files *yaml.Node, path string) bool {
	for _, f := range files.Content {
		if f.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(f.Content); i += 2 {
			if f.Content[i].Value == "path" && f.Content[i+1].Value == path {
				return true
			}
		}
	}
	return false
}

func rotateFile(path, perm, content string) *yaml.Node {
	str := func(v string, style yaml.Style) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: style}
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		str("path", 0), str(path, 0),
		str("owner", 0), str("root:root", 0),
		str("permissions", 0), str(perm, yaml.DoubleQuotedStyle),
		str("content", 0), str(content, yaml.LiteralStyle),
	}}
}

const hostKeyTool = "/usr/local/sbin/spinup-host-key"
