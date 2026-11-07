package project

import (
	"os"
	"testing"
)

func TestBillingSeedBytes(t *testing.T) {
	var c Config
	d := c.BillingConfig()
	for path, want := range map[string][]byte{
		"../templates/packs/billing/files/" + BillingFilePath:   RenderBillingFile(d),
		"../templates/packs/billing/files/" + BillingTSFilePath: RenderBillingTSFile(d),
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
