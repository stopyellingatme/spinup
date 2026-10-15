package devloop

import (
	"fmt"
	"syscall"
)

func processAttributes() *syscall.SysProcAttr { return nil }
func signalGroup(_ int, _ bool) error {
	return fmt.Errorf("dev requires Unix process groups\nTry: use WSL2 on Windows")
}

func groupAlive(_ int) bool { return false }
