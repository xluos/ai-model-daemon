//go:build windows

package server

import (
	"fmt"
	"net"
)

// listenIPC 在 Windows 平台用 localhost TCP 动态空闲端口监听（go-winio 不是依赖）。
// addr 参数被忽略；dialSpec 形如 "tcp:127.0.0.1:<port>"，供复用探测与集成方连接。
func listenIPC(_ string) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return ln, fmt.Sprintf("tcp:127.0.0.1:%d", port), nil
}

func cleanupIPC(_ string) {
	// TCP listeners don't leave files to clean up.
}
