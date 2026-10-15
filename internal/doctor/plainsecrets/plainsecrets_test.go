package plainsecrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestPlainSecrets(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, tracked, content, ignore string
		want                           doctor.Status
	}{
		{"encrypted", "secrets/staging.enc.yaml", "sops:\n  age: []\n", ".env\n.env.*\n", doctor.OK},
		{"multiple documents", "secrets/plain.yaml", "sops:\n  age: []\n---\nPASSWORD: do-not-expose\n", ".env\n.env.*\n", doctor.Fail},
		{"empty metadata", "secrets/plain.yaml", "sops: {}\nPASSWORD: do-not-expose\n", ".env\n.env.*\n", doctor.Fail},
		{"plaintext", "secrets/plain.yaml", "PASSWORD: do-not-expose\n", ".env\n.env.*\n", doctor.Fail},
		{"fake metadata", "secrets/plain.yaml", "PASSWORD: 'text sops: do-not-expose'\n", ".env\n.env.*\n", doctor.Fail},
		{"age key", "keys/recovery.agekey", "AGE-SECRET-KEY-do-not-expose", ".env\n.env.*\n", doctor.Fail},
		{"dotenv", "api/.env", "PASSWORD=do-not-expose", ".env\n.env.*\n", doctor.Fail},
		{"dotenv variant", "api/.env.production", "PASSWORD=do-not-expose", ".env\n.env.*\n", doctor.Fail},
		{"dotenv example", "api/.env.example", "PASSWORD=example", ".env\n.env.*\n", doctor.OK},
		{"missing ignore", "README.md", "safe", "node_modules/\n", doctor.Fail},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, tt.tracked)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(tt.ignore), 0600); err != nil {
				t.Fatal(err)
			}
			f := &runexec.Fake{}
			f.Expect("git", "ls-files", "-z").Return(tt.tracked+"\x00", "", 0)
			if tt.name == "encrypted" {
				f.Expect("git", "show", ":./"+tt.tracked).Return(tt.content, "", 0)
			}
			c := &doctor.Context{Root: root, Deps: map[string]any{"runner": f}}
			result := runCheck(context.Background(), c)
			if result.Status != tt.want || strings.Contains(result.Message, "do-not-expose") || result.Status == doctor.Fail && result.Try == "" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestQuickSkips(t *testing.T) {
	t.Parallel()
	for _, c := range []*doctor.Context{{}, {Root: t.TempDir(), Quick: true}} {
		if result := runCheck(context.Background(), c); result.Status != doctor.Skip {
			t.Fatalf("result=%+v", result)
		}
	}
}

func TestStagedPlaintext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets/staging.enc.yaml"), []byte("sops:\n  age: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\n.env.*\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &runexec.Fake{}
	f.Expect("git", "ls-files", "-z").Return("secrets/staging.enc.yaml\x00", "", 0)
	f.Expect("git", "show", ":./secrets/staging.enc.yaml").Return("PASSWORD: do-not-expose\n", "", 0)
	result := runCheck(context.Background(), &doctor.Context{Root: root, Deps: map[string]any{"runner": f}})
	if result.Status != doctor.Fail || !strings.Contains(result.Message, "staged plaintext") || strings.Contains(result.Message, "do-not-expose") {
		t.Fatalf("result=%+v", result)
	}
	f.AssertDone(t)
}
