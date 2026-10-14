//go:build windows

package exec

import osexec "os/exec"

func configureProcess(_ *osexec.Cmd, _ bool) {}

func killProcess(command *osexec.Cmd) error {
	// Windows kills only the direct child. Descendants can survive cancellation.
	if command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
