//go:build windows

package runtime

import (
	"os"
	"strings"
)

// execCandidates 返回在 PATH 中查找可执行文件时应尝试的候选名。
// Windows 下 python 等命令需带可执行后缀（.exe/.bat/.cmd），否则裸名 Stat 永远失败。
// 优先按 PATHEXT 生成候选，并把裸名追加末尾兜底。
func execCandidates(name string) []string {
	pathext := os.Getenv("PATHEXT")
	var cands []string
	if pathext != "" {
		for _, ext := range strings.Split(pathext, ";") {
			ext = strings.TrimSpace(ext)
			if ext == "" {
				continue
			}
			cands = append(cands, name+strings.ToLower(ext))
		}
	}
	if len(cands) == 0 {
		cands = []string{name + ".exe", name + ".bat", name + ".cmd"}
	}
	cands = append(cands, name) // 兜底裸名
	return cands
}
