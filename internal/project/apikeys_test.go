package project

import (
	"os"
	"testing"
)

func TestAPIKeysSeedBytes(t *testing.T) {
	var c Config
	d := c.APIKeysConfig()
	for path, want := range map[string][]byte{
		"../templates/packs/apikeys/files/" + APIKeysFilePath:   RenderAPIKeysFile(d),
		"../templates/packs/apikeys/files/" + APIKeysTSFilePath: RenderAPIKeysTSFile(d, c.AuthRoles()),
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s:\n%s\nwant:\n%s", path, got, want)
		}
	}
}
