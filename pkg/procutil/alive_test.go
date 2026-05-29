package procutil

import (
	"os"
	"testing"
)

// TestIsAliveCurrentProcess 在所有平台下都应判定当前进程存活。
// 注意：开发机通常为 macOS（走 alive_other.go），无法暴露 Windows 实现的问题。
// Windows 实现（alive_windows.go）依赖 OpenProcess 时带上 SYNCHRONIZE 访问权，
// 否则 WaitForSingleObject 返回 WAIT_FAILED 会让本用例对自身 PID 也失败。
// 建议在 CI 上额外用 GOOS=windows 编译并运行本测试以防回归。
func TestIsAliveCurrentProcess(t *testing.T) {
	if !IsAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
}

func TestIsAliveInvalidPID(t *testing.T) {
	if IsAlive(-1) {
		t.Error("pid -1 should not be alive")
	}
	if IsAlive(0) {
		t.Error("pid 0 should not be alive")
	}
}
