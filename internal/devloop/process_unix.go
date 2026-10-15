//go:build !windows

package devloop

import "syscall"

func processAttributes() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
func signalGroup(pid int, kill bool) error {
	signal := syscall.SIGTERM
	if kill {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(-pid, signal)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func groupAlive(pid int) bool { return syscall.Kill(-pid, 0) != syscall.ESRCH }
