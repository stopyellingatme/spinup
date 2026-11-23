package deploy

import (
	"context"
	"errors"
	"maps"
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

// TestUploaderKeepsPathsThatHoldASecretValue checks that a secret value which is also part
// of the remote path does not stop the push. A bucket secret with the name of the app made
// the find output "/opt/[REDACTED]/secrets/...", and the push failed with "unexpected entry".
func TestUploaderKeepsPathsThatHoldASecretValue(t *testing.T) {
	t.Parallel()
	for _, caller := range []string{"deploy upload", "secrets push"} {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			x.decrypted = maps.Clone(testSecrets)
			x.decrypted["BUCKET"] = "todo"
			x.decrypted["SECRET_NAME"] = "DATABASE_URL"
			var f *flow
			if caller == "deploy upload" {
				x.expectLocalGit()
				x.o.fill()
				l, err := preflight(t.Context(), x.o, x.d, &x.out, false)
				if err != nil {
					t.Fatal(err)
				}
				f = &flow{o: x.o, d: x.d, l: l, vm: x.vm, out: &x.out, release: testRelease, redact: redactValues(x.decrypted)}
				x.expectUpstream("")
				x.ok("mkdir", "-p", "--", testRelDir)
				files, err := f.releaseFiles()
				if err != nil {
					t.Fatal(err)
				}
				for _, file := range files {
					x.vm.ExpectUpload(testRelDir + "/" + file.Path)
				}
			} else {
				x.expectLock()
			}
			x.ok("sh")
			x.vm.Expect("find", testRoot+"/secrets").Return(testRoot+"/secrets/DATABASE_URL\n"+testRoot+"/secrets/OLD\n", "", 0)
			for _, name := range secretNames(x.decrypted) {
				x.vm.ExpectUpload(testRoot + "/secrets/" + name)
			}
			x.ok("rm", "-f", "--", testRoot+"/secrets/OLD")
			if f != nil {
				x.vm.ExpectUpload(testRelDir + "/release.json")
				if _, err := f.upload(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else {
				x.expectUnlockAndClose()
				if err := PushSecrets(t.Context(), x.o, x.d); err != nil {
					t.Fatal(err)
				}
			}
			x.vm.AssertDone(t)
			x.exec.AssertDone(t)
			for _, call := range x.vm.Calls() {
				if call.Argv[0] == "find" && len(call.Redact) != 0 {
					t.Errorf("find redacts values, but its output contains paths: %v", call.Redact)
				}
			}
		})
	}
}
