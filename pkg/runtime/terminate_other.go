//go:build !windows

package runtime

import (
	"os"
	"os/exec"
)

// terminateGraceful 向子进程发送优雅终止信号。
// 非 Windows 平台发送 SIGINT (os.Interrupt)，由调用方等待退出或超时后 Kill。
func terminateGraceful(cmd *exec.Cmd) error {
	return cmd.Process.Signal(os.Interrupt)
}
