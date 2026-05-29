package hardware

import (
	"os"
	"runtime"
	"testing"
)

var gpuHintVars = []string{
	"CUDA_VISIBLE_DEVICES", "NVIDIA_VISIBLE_DEVICES",
	"ROC_ENABLE_PRE_VEGA", "HSA_OVERRIDE_GFX_VERSION",
}

// clearGPUHints unsets every GPU hint env var and restores them after the test.
// detectBackend uses os.LookupEnv (presence-based), so the vars must be truly
// unset rather than set to an empty string.
func clearGPUHints(t *testing.T) {
	t.Helper()
	for _, e := range gpuHintVars {
		if orig, ok := os.LookupEnv(e); ok {
			os.Unsetenv(e)
			e, orig := e, orig
			t.Cleanup(func() { os.Setenv(e, orig) })
		}
	}
}

func TestDetectBackendAppleSilicon(t *testing.T) {
	if got := detectBackend(true); got != BackendMetal {
		t.Errorf("Apple Silicon backend = %q, want metal", got)
	}
}

func TestDetectBackendEnvHints(t *testing.T) {
	tests := []struct {
		env  string
		want Backend
	}{
		{"CUDA_VISIBLE_DEVICES", BackendCUDA},
		{"NVIDIA_VISIBLE_DEVICES", BackendCUDA},
		{"ROC_ENABLE_PRE_VEGA", BackendROCm},
		{"HSA_OVERRIDE_GFX_VERSION", BackendROCm},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			clearGPUHints(t)
			t.Setenv(tt.env, "1")
			if got := detectBackend(false); got != tt.want {
				t.Errorf("env %s → backend %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}

func TestDetectBackendCPUFallback(t *testing.T) {
	clearGPUHints(t)
	if got := detectBackend(false); got != BackendCPU {
		t.Errorf("no GPU hints → backend %q, want cpu", got)
	}
}

func TestBackendSpeedConstants(t *testing.T) {
	want := map[Backend]int{
		BackendMetal: 160,
		BackendCUDA:  220,
		BackendROCm:  180,
		BackendCPU:   80,
	}
	for b, k := range want {
		if backendSpeedK[b] != k {
			t.Errorf("speed constant for %q = %d, want %d", b, backendSpeedK[b], k)
		}
	}
}

func TestDetectInvariants(t *testing.T) {
	info := Detect()

	if info.Platform != runtime.GOOS {
		t.Errorf("Platform = %q, want %q", info.Platform, runtime.GOOS)
	}
	if info.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want %q", info.Arch, runtime.GOARCH)
	}
	if info.AvailableMemoryBytes < 0 {
		t.Errorf("AvailableMemoryBytes negative: %d", info.AvailableMemoryBytes)
	}
	if info.TotalMemoryBytes > 0 && info.AvailableMemoryBytes > info.TotalMemoryBytes {
		t.Errorf("available (%d) should not exceed total (%d)",
			info.AvailableMemoryBytes, info.TotalMemoryBytes)
	}
	if info.SpeedConstant != backendSpeedK[info.Backend] {
		t.Errorf("SpeedConstant %d does not match backend %q (%d)",
			info.SpeedConstant, info.Backend, backendSpeedK[info.Backend])
	}
	if info.CPUModel == "" {
		t.Error("CPUModel should not be empty")
	}

	validQuants := map[string]bool{"Q3_K_M": true, "Q4_K_M": true, "Q5_K_M": true, "Q6_K": true}
	if !validQuants[info.RecommendedQuant] {
		t.Errorf("RecommendedQuant %q not in expected set", info.RecommendedQuant)
	}

	// On Apple Silicon the GPU cap should be set and below total memory.
	if info.IsAppleSilicon {
		if info.Backend != BackendMetal {
			t.Errorf("Apple Silicon backend = %q, want metal", info.Backend)
		}
		if info.GPUMemoryCapBytes <= 0 || info.GPUMemoryCapBytes > info.TotalMemoryBytes {
			t.Errorf("GPU cap %d out of range (total %d)", info.GPUMemoryCapBytes, info.TotalMemoryBytes)
		}
	}
}
