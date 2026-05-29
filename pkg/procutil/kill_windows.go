//go:build windows

package procutil

import "os"

// Kill 终止给定 pid 的进程。
// Windows 上 os.Process.Signal 除 Kill 外一律返回 not supported，
// 发送 SIGTERM 是 no-op，旧进程不会退出。因此改用 os.Process.Kill()，
// 其底层为 TerminateProcess，可真正结束目标进程。
func Kill(pid int) error {
	if pid <= 0 {
		return os.ErrInvalid
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
