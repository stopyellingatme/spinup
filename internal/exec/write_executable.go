package exec

import (
	"io/fs"
	"os"
	"syscall"
)

// WriteExecutable writes an executable file. It holds syscall.ForkLock for writing, so no child
// process forks while the file is open for writing and inherits its descriptor (golang/go#22315).
func WriteExecutable(path string, data []byte, perm fs.FileMode) error {
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	return os.WriteFile(path, data, perm)
}
