package vfs

import (
	"os"
	"syscall"
)

func syncFile(file *os.File) error {
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var syncErr error
	if err := conn.Control(func(fd uintptr) {
		syncErr = syscall.Fsync(int(fd))
	}); err != nil {
		return err
	}
	return syncErr
}

func syncBatch(root string) error {
	// Flushing the root also covers batches with only deletions or mode changes.
	file, err := os.Open(root)
	if err != nil {
		return err
	}
	defer file.Close()
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var syncErr error
	if err := conn.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_FULLFSYNC, 0)
		switch errno {
		case 0:
		case syscall.ENOTSUP, syscall.EINVAL, syscall.ENOTTY:
			// Some file systems (network, FAT) do not support F_FULLFSYNC.
			// os.File.Sync falls back to fsync in the same case.
			syncErr = syscall.Fsync(int(fd))
		default:
			syncErr = os.NewSyscallError("fcntl F_FULLFSYNC", errno)
		}
	}); err != nil {
		return err
	}
	return syncErr
}
