package hardware

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func totalMemoryBytes() int64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 16 * 1024 * 1024 * 1024
	}
	return int64(info.Totalram) * int64(info.Unit)
}

func cpuModelPlatform() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	_ = strconv.Itoa(0)
	return ""
}
