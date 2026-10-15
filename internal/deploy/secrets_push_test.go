package deploy

import (
	"context"
	"errors"
	"testing"
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
