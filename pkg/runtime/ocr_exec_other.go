//go:build !windows

package runtime

// execCandidates 返回在 PATH 中查找可执行文件时应尝试的候选名。
// 非 Windows 平台无需可执行后缀，直接返回原名。
func execCandidates(name string) []string {
	return []string{name}
}
