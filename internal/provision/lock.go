package provision

import (
	"fmt"
	"os"
	"path/filepath"
)

// lockEnv makes sure that only one provisioning or destroy run works on the environment of
// this project at a time. Two runs would both rotate the host key and race on the droplet.
func (b *base) lockEnv() (func(), error) {
	dir := filepath.Join(b.root, ".spinup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, failWrap("lock the environment", err, "check the permissions of "+dir)
	}
	path := filepath.Join(dir, "provision-"+b.env+".lock")
	unlock, ok, err := tryLock(path)
	if err != nil {
		return nil, failWrap("lock the environment", err, "check the permissions of "+path)
	}
	if !ok {
		return nil, fail("lock the environment",
			fmt.Sprintf("another spinup deploy digital-ocean run works on the environment %s (lock %s)", b.env, path),
			"wait until the other run ends, then run the command again")
	}
	return unlock, nil
}
