package provision

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/server"
)

const cloudConfigHeader = "#cloud-config"

// buildUserData renders the cloud-init document with the host key in ssh_keys.
// The result holds a private key. Callers must never print it or put it in an error.
func buildUserData(root, app, operatorPub, hostPriv, hostPub string) (string, error) {
	raw, err := server.CloudInit(root, operatorPub)
	if err != nil {
		return "", failWrap("render the cloud-init file", err, "check deploy/cloud-init/user-data.yaml")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", fail("parse the cloud-init file", "the YAML is not valid", "check deploy/cloud-init/user-data.yaml")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fail("parse the cloud-init file", "the document is not a mapping", "check deploy/cloud-init/user-data.yaml")
	}
	keys := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		scalar("ed25519_private", 0), scalar(hostPriv, yaml.LiteralStyle),
		scalar("ed25519_public", 0), scalar(hostPub, 0),
	}}
	setKey(doc.Content[0], "ssh_keys", keys)
	if err := addRotationTooling(&doc, app); err != nil {
		return "", fail("add the host key tooling to the cloud-init file", err.Error(), "check deploy/cloud-init/user-data.yaml")
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", fail("write the cloud-init file", "the YAML encoder failed", "run the command again")
	}
	if err := enc.Close(); err != nil {
		return "", fail("write the cloud-init file", "the YAML encoder failed", "run the command again")
	}
	out := buf.String()
	if !strings.HasPrefix(out, cloudConfigHeader) {
		out = cloudConfigHeader + "\n" + out
	}
	return out, nil
}

func scalar(value string, style yaml.Style) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
}

// setKey sets a key of a mapping node and keeps the order of the other keys.
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, scalar(key, 0), value)
}
