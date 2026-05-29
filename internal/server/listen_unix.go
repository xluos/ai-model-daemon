//go:build !windows

package server

import (
	"net"
	"os"
)

// listenIPC 在 Unix 平台监听 Unix domain socket，返回监听器与拨号信息。
// dialSpec 形如 "unix:<absolute-path>"，供复用探测与集成方连接。
func listenIPC(addr string) (net.Listener, string, error) {
	_ = os.Remove(addr)
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, "", err
	}
	_ = os.Chmod(addr, 0o600)
	return ln, "unix:" + addr, nil
}

func cleanupIPC(addr string) {
	_ = os.Remove(addr)
}
