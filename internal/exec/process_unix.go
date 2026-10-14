//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package exec

import (
	osexec "os/exec"
	"syscall"
)

func configureProcess(command *osexec.Cmd, interactive bool) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: !interactive}
}

func killProcess(command *osexec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		return command.Process.Kill()
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
