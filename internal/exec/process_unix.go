//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package exec

import (
	osexec "os/exec"
	"syscall"
)

func configureProcess(command *osexec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcess(command *osexec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
