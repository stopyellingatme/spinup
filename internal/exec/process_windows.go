//go:build windows

package exec

import osexec "os/exec"

func configureProcess(command *osexec.Cmd) {}

func killProcess(command *osexec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
