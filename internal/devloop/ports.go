package devloop

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// FreePort returns the preferred loopback TCP port, or an available alternative.
// The caller must bind the returned port promptly. The reservation ends on return.
func FreePort(preferred int) (int, error) {
	var config net.ListenConfig
	return freePort(preferred, func(network, address string) (net.Listener, error) {
		return config.Listen(context.Background(), network, address)
	})
}

func freePort(preferred int, listen func(string, string) (net.Listener, error)) (int, error) {
	if preferred < 0 || preferred > 65535 {
		return 0, fmt.Errorf("allocate port %d: outside 0 through 65535\nTry: choose a valid TCP port", preferred)
	}
	listener, err := listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(preferred)))
	if err != nil && preferred != 0 {
		listener, err = listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return 0, fmt.Errorf("allocate local TCP port: %w\nTry: release a local port and repeat spinup dev", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release local TCP port: %w\nTry: repeat spinup dev", err)
	}
	return port, nil
}
