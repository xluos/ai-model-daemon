//go:build windows

package runtime

import "os/exec"

// terminateGraceful 终止子进程。
// Windows 无可靠的优雅信号通道（os.Process.Signal 除 Kill 外返回 not supported），
// 直接 Kill 以避免空等满 StopTimeout。
func terminateGraceful(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
