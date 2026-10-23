package provision

import "go.yaml.in/yaml/v3"

// addRotationTooling adds the spinup-host-key script and its sudoers rule to a parsed cloud-init
// document (ADR 0017 §6). Unit U5b replaces this stub.
func addRotationTooling(doc *yaml.Node, app string) error {
	_, _ = doc, app
	return nil
}
