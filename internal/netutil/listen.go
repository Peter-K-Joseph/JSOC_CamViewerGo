package netutil

import (
	"fmt"
	"net"
)

// ListenTCP binds to host:port, incrementing port until one is free.
// Returns the listener and the actual port bound.
func ListenTCP(host string, startPort int) (net.Listener, int, error) {
	for port := startPort; port < startPort+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, fmt.Errorf("no free TCP port found in range %d-%d", startPort, startPort+99)
}
