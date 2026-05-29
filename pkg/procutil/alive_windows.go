//go:build windows

package procutil

import "syscall"

// PROCESS_QUERY_LIMITED_INFORMATION 不需要提权即可跨完整性级别查询进程，
// 标准库 syscall 包未导出该常量，这里本地定义其字面量。
const processQueryLimitedInformation = 0x1000

// IsAlive 判断给定 pid 的进程是否存活。
// Windows 上 os.Process.Signal 除 Kill 外一律返回 not supported，
// 因此改用 OpenProcess + WaitForSingleObject：
//   - OpenProcess 失败 => 进程不存在 => 已死
//   - WaitForSingleObject(h, 0) == WAIT_TIMEOUT(258) => 句柄未 signaled => 存活
//   - == WAIT_OBJECT_0 => 进程已退出 => 已死
//
// desiredAccess 必须同时包含 SYNCHRONIZE 和 PROCESS_QUERY_LIMITED_INFORMATION：
// WaitForSingleObject 要求句柄具备 SYNCHRONIZE 访问权，否则返回 WAIT_FAILED
// (0xFFFFFFFF) 且 GetLastError=ERROR_ACCESS_DENIED，Go 的 syscall.WaitForSingleObject
// 会因此置 err!=nil，导致对存活进程也误判为已死。
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.SYNCHRONIZE|processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	ev, err := syscall.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return ev == uint32(syscall.WAIT_TIMEOUT)
}
