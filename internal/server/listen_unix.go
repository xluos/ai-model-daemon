//go:build !windows

package server

import (
	"net"
	"os"
)

func listenIPC(addr string) (net.Listener, error) {
	_ = os.Remove(addr)
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(addr, 0o600)
	return ln, nil
}

func cleanupIPC(addr string) {
	_ = os.Remove(addr)
}
