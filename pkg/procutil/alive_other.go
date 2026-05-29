//go:build !windows

package procutil

import (
	"os"
	"syscall"
)

// IsAlive 判断给定 pid 的进程是否存活。
// 非 Windows 平台沿用向进程发送 signal 0 的惯用法：成功即存活。
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
