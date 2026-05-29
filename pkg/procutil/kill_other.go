//go:build !windows

package procutil

import (
	"os"
	"syscall"
)

// Kill 终止给定 pid 的进程。
// 非 Windows 平台优先发送 SIGTERM 以触发目标进程的优雅关停逻辑，
// 由调用方在返回后自行等待，必要时再升级到 SIGKILL。
func Kill(pid int) error {
	if pid <= 0 {
		return os.ErrInvalid
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
