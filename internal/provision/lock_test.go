package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpRefusesASecondRunOfTheEnvironment(t *testing.T) {
	f := newFixture(t, nil)
	dir := filepath.Join(f.root, ".spinup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Another run holds the lock of the environment.
	unlock, ok, err := tryLock(filepath.Join(dir, "provision-production.lock"))
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	err = f.up()
	unlock()
	if err == nil || !strings.Contains(err.Error(), "another spinup deploy digital-ocean run works on the environment production") {
		t.Fatalf("err = %v", err)
	}
	if len(f.srv.Droplets()) != 0 {
		t.Fatal("the second run created a droplet")
	}
	// After the other run ends, the next run works.
	if err := f.up(); err != nil {
		t.Fatal(err)
	}
}
