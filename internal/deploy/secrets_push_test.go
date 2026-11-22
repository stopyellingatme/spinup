package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/secrets"
)

func TestPushSecretsNeedsAProject(t *testing.T) {
	t.Parallel()
	err := PushSecrets(context.Background(), Options{Dir: t.TempDir()}, Deps{Decrypt: func(context.Context, string, string) (map[string]string, error) {
		return nil, errors.New("not reached")
	}})
	if err == nil {
		t.Fatal("PushSecrets outside a project returned nil")
	}
}

// TestUploaderKeepsPathsThatHoldASecretValue checks that a secret value which is also part
// of the remote path does not stop the push. A bucket secret with the name of the app made
// the find output "/opt/[REDACTED]/secrets/...", and the push failed with "unexpected entry".
func TestUploaderKeepsPathsThatHoldASecretValue(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	vm.Expect("sh").Return("", "", 0)
	vm.Expect("find").Return("/opt/demo/secrets/DATABASE_URL\n/opt/demo/secrets/OLD\n", "", 0)
	vm.ExpectUpload("/opt/demo/secrets/BUCKET").Return("", "", 0)
	vm.ExpectUpload("/opt/demo/secrets/DATABASE_URL").Return("", "", 0)
	vm.Expect("rm", "-f", "--", "/opt/demo/secrets/OLD").Return("", "", 0)
	values := map[string]string{"BUCKET": "demo", "DATABASE_URL": "URL"}
	if err := secrets.Push(context.Background(), uploader{vm: vm}, "demo", values); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	vm.AssertDone(t)
	for _, call := range vm.Calls() {
		if len(call.Redact) != 0 {
			t.Errorf("remote command %v redacts %d values, and its output is parsed as paths", call.Argv[:1], len(call.Redact))
		}
	}
}
