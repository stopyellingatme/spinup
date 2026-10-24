package exec

import (
	"io/fs"
	"os"
	"syscall"
)

// WriteExecutable writes an executable file and syncs it to disk. It holds syscall.ForkLock for
// writing, so no child process forks while the file is open for writing and inherits its
// descriptor (golang/go#22315). An exec of the file then never fails with "text file busy".
func WriteExecutable(path string, data []byte, perm fs.FileMode) error {
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
