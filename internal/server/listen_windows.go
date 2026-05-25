//go:build windows

package server

import (
	"net"
)

const pipeName = `\\.\pipe\ai-model-daemon`

func listenIPC(_ string) (net.Listener, error) {
	// On Windows use localhost TCP since go-winio is not a dependency here.
	// The addr argument is ignored; we bind to a fixed local port.
	return net.Listen("tcp", "127.0.0.1:19821")
}

func cleanupIPC(_ string) {
	// TCP listeners don't leave files to clean up.
}
