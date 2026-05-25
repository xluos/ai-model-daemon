package hardware

import (
	"os"
	"runtime"
	"strings"
)

type Backend string

const (
	BackendMetal Backend = "metal"
	BackendCUDA  Backend = "cuda"
	BackendROCm  Backend = "rocm"
	BackendCPU   Backend = "cpu"
)

type MachineInfo struct {
	Platform            string  `json:"platform"`
	Arch                string  `json:"arch"`
	TotalMemoryBytes    int64   `json:"totalMemoryBytes"`
	AvailableMemoryBytes int64  `json:"availableMemoryBytes"`
	GPUMemoryCapBytes   int64   `json:"gpuMemoryCapBytes"`
	IsAppleSilicon      bool    `json:"isAppleSilicon"`
	CPUModel            string  `json:"cpuModel"`
	Backend             Backend `json:"backend"`
	SpeedConstant       int     `json:"speedConstant"`
	RecommendedQuant    string  `json:"recommendedQuant"`
}

var backendSpeedK = map[Backend]int{
	BackendMetal: 160,
	BackendCUDA:  220,
	BackendROCm:  180,
	BackendCPU:   80,
}

func Detect() MachineInfo {
	platform := runtime.GOOS
	arch := runtime.GOARCH
	totalMem := totalMemoryBytes()
	cpuModel := cpuModelName()
	isAppleSilicon := platform == "darwin" && arch == "arm64"

	// System reserved: Apple Silicon 20% (cap 6GB), others 6GB flat.
	var systemReserved int64
	if isAppleSilicon {
		systemReserved = int64(float64(totalMem) * 0.2)
		cap6gb := int64(6) * 1024 * 1024 * 1024
		if systemReserved > cap6gb {
			systemReserved = cap6gb
		}
	} else {
		systemReserved = 6 * 1024 * 1024 * 1024
	}
	available := totalMem - systemReserved
	if available < 0 {
		available = 0
	}

	// GPU memory cap: Apple Silicon Metal wired limit ≈ totalMem × 67%.
	var gpuMemCap int64
	if isAppleSilicon {
		gpuMemCap = int64(float64(totalMem) * 0.67)
	} else {
		gpuMemCap = available
	}

	backend := detectBackend(isAppleSilicon)
	speedK := backendSpeedK[backend]

	availGB := float64(available) / (1024 * 1024 * 1024)
	var recQuant string
	switch {
	case availGB < 8:
		recQuant = "Q3_K_M"
	case availGB < 16:
		recQuant = "Q4_K_M"
	case availGB < 32:
		recQuant = "Q5_K_M"
	default:
		recQuant = "Q6_K"
	}

	return MachineInfo{
		Platform:             platform,
		Arch:                 arch,
		TotalMemoryBytes:     totalMem,
		AvailableMemoryBytes: available,
		GPUMemoryCapBytes:    gpuMemCap,
		IsAppleSilicon:       isAppleSilicon,
		CPUModel:             cpuModel,
		Backend:              backend,
		SpeedConstant:        speedK,
		RecommendedQuant:     recQuant,
	}
}

func detectBackend(isAppleSilicon bool) Backend {
	if isAppleSilicon {
		return BackendMetal
	}
	// Check for NVIDIA GPU via env hints.
	if _, ok := os.LookupEnv("CUDA_VISIBLE_DEVICES"); ok {
		return BackendCUDA
	}
	if _, ok := os.LookupEnv("NVIDIA_VISIBLE_DEVICES"); ok {
		return BackendCUDA
	}
	if _, ok := os.LookupEnv("ROC_ENABLE_PRE_VEGA"); ok {
		return BackendROCm
	}
	if _, ok := os.LookupEnv("HSA_OVERRIDE_GFX_VERSION"); ok {
		return BackendROCm
	}
	return BackendCPU
}

func cpuModelName() string {
	name := cpuModelPlatform()
	if name == "" {
		return "Unknown CPU"
	}
	return strings.TrimSpace(name)
}
