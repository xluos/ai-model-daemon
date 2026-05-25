package hardware

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func totalMemoryBytes() int64 {
	mib := [2]int32{6 /* CTL_HW */, 24 /* HW_MEMSIZE */}
	var size uint64
	n := uintptr(8)
	_, _, errno := syscall.Syscall6(
		syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		2,
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&n)),
		0, 0,
	)
	if errno != 0 {
		return fallbackTotalMem()
	}
	return int64(size)
}

func fallbackTotalMem() int64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 16 * 1024 * 1024 * 1024 // 16GB fallback
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 16 * 1024 * 1024 * 1024
	}
	return n
}

func cpuModelPlatform() string {
	out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
