package fit

import (
	"testing"

	"github.com/xluos/ai-model-daemon/pkg/hardware"
	"github.com/xluos/ai-model-daemon/pkg/manifest"
)

func TestParseParams(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"4B", 4},
		{"0.8B", 0.8},
		{"35B-A3B", 35},
		{"7 B", 7},
		{"27B", 27},
		{"", 0},
		{"unknown", 0},
		{"B", 0},
		{"1.7B", 1.7},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ParseParams(tt.in); got != tt.want {
				t.Errorf("ParseParams(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFitRank(t *testing.T) {
	// Ranks must be strictly ordered best→worst so SortByFit is stable.
	order := []FitLevel{FitPerfect, FitGood, FitMarginal, FitTight}
	for i := 1; i < len(order); i++ {
		if FitRank(order[i-1]) >= FitRank(order[i]) {
			t.Errorf("FitRank(%s)=%d should be < FitRank(%s)=%d",
				order[i-1], FitRank(order[i-1]), order[i], FitRank(order[i]))
		}
	}
	if FitRank(FitLevel("garbage")) != 4 {
		t.Errorf("unknown fit level should rank worst (4), got %d", FitRank(FitLevel("garbage")))
	}
}

func TestEstimateKVBytesPerToken(t *testing.T) {
	// Explicit entry value wins over heuristic.
	entry := &manifest.Model{KVBytesPerToken: 9999}
	if got := estimateKVBytesPerToken(entry, 4); got != 9999 {
		t.Errorf("explicit KVBytesPerToken ignored: got %d", got)
	}

	// Exact param match returns the table value.
	if got := estimateKVBytesPerToken(nil, 4); got != 60*1024 {
		t.Errorf("estimate for 4B = %d, want %d", got, 60*1024)
	}

	// Nearest-neighbor: 5 is closer to 4 (table) than to 7.
	if got := estimateKVBytesPerToken(nil, 5); got != 60*1024 {
		t.Errorf("estimate for 5B should snap to 4B bucket, got %d", got)
	}

	// Zero params with no entry falls back to the default.
	if got := estimateKVBytesPerToken(nil, 0); got != 64*1024 {
		t.Errorf("fallback estimate = %d, want %d", got, 64*1024)
	}
}

func TestComputeQuantFitLevels(t *testing.T) {
	// Machine with a 100 GB cap makes the math easy to reason about.
	const gb = int64(1024 * 1024 * 1024)
	machine := hardware.MachineInfo{
		GPUMemoryCapBytes: 100 * gb,
		SpeedConstant:     160,
	}

	tests := []struct {
		name      string
		weight    int64
		wantLevel FitLevel
	}{
		{"perfect <50%", 10 * gb, FitPerfect},
		{"good 50-75%", 60 * gb, FitGood},
		{"marginal 75-95%", 85 * gb, FitMarginal},
		{"tight >=95%", 96 * gb, FitTight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use tiny ctx + KV so weight dominates; entry sets KVBytesPerToken=1.
			entry := &manifest.Model{KVBytesPerToken: 1, ContextSize: 1}
			res := ComputeQuantFit(tt.weight, "Q4_K_M", machine, 4, 1, entry)
			if res.Fit != tt.wantLevel {
				t.Errorf("weight %d GB → fit %s (%.1f%%), want %s",
					tt.weight/gb, res.Fit, float64(res.MemPercent), tt.wantLevel)
			}
		})
	}
}

func TestComputeQuantFitNoMemCap(t *testing.T) {
	machine := hardware.MachineInfo{} // no GPU cap, no available memory
	res := ComputeQuantFit(1000, "Q4_K_M", machine, 4, 100, nil)
	if res.Fit != FitTight {
		t.Errorf("unknown memory should yield tight, got %s", res.Fit)
	}
	if res.MemPercent != 0 {
		t.Errorf("memPercent should be 0 when cap unknown, got %d", res.MemPercent)
	}
}

func TestComputeQuantFitFallsBackToAvailableMemory(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	machine := hardware.MachineInfo{AvailableMemoryBytes: 100 * gb, SpeedConstant: 160}
	entry := &manifest.Model{KVBytesPerToken: 1, ContextSize: 1}
	res := ComputeQuantFit(10*gb, "Q4_K_M", machine, 4, 1, entry)
	if res.MemCapBytes != 100*gb {
		t.Errorf("should fall back to AvailableMemoryBytes, got %d", res.MemCapBytes)
	}
	if res.Fit != FitPerfect {
		t.Errorf("10/100 GB should be perfect, got %s", res.Fit)
	}
}

func TestComputeQuantFitContextDefaults(t *testing.T) {
	machine := hardware.MachineInfo{GPUMemoryCapBytes: 100 * 1024 * 1024 * 1024}

	// effectiveCtx <= 0 with entry.ContextSize set → uses entry context.
	entry := &manifest.Model{ContextSize: 4096}
	res := ComputeQuantFit(1000, "", machine, 1, 0, entry)
	if res.EffectiveCtx != 4096 {
		t.Errorf("EffectiveCtx = %d, want entry context 4096", res.EffectiveCtx)
	}

	// effectiveCtx <= 0 and no entry → default 8192.
	res = ComputeQuantFit(1000, "", machine, 1, 0, nil)
	if res.EffectiveCtx != 8192 {
		t.Errorf("EffectiveCtx = %d, want default 8192", res.EffectiveCtx)
	}
}

func TestComputeQuantFitTPS(t *testing.T) {
	machine := hardware.MachineInfo{GPUMemoryCapBytes: 100 * 1024 * 1024 * 1024, SpeedConstant: 160}

	// TPS = K / paramsB * quantMult. Q4_K_M mult = 1.0 → 160/4 = 40.
	res := ComputeQuantFit(1000, "Q4_K_M", machine, 4, 100, nil)
	if res.TPS != 40 {
		t.Errorf("TPS for 4B Q4_K_M = %d, want 40", res.TPS)
	}

	// Q8_0 mult = 0.6 → round(160/4*0.6) = 24.
	res = ComputeQuantFit(1000, "Q8_0", machine, 4, 100, nil)
	if res.TPS != 24 {
		t.Errorf("TPS for 4B Q8_0 = %d, want 24", res.TPS)
	}

	// Unknown quant label → mult defaults to 1.0.
	res = ComputeQuantFit(1000, "Q_UNKNOWN", machine, 4, 100, nil)
	if res.TPS != 40 {
		t.Errorf("TPS for unknown quant = %d, want 40 (mult 1.0)", res.TPS)
	}

	// Zero params → TPS 0 (cannot estimate).
	res = ComputeQuantFit(1000, "Q4_K_M", machine, 0, 100, nil)
	if res.TPS != 0 {
		t.Errorf("TPS for 0 params = %d, want 0", res.TPS)
	}
}

func TestComputeQuantFitMemBreakdown(t *testing.T) {
	machine := hardware.MachineInfo{GPUMemoryCapBytes: 1 << 40}
	entry := &manifest.Model{KVBytesPerToken: 1000, ContextSize: 10}
	res := ComputeQuantFit(2000, "Q4_K_M", machine, 4, 10, entry)

	if res.WeightBytes != 2000 {
		t.Errorf("WeightBytes = %d, want 2000", res.WeightBytes)
	}
	if res.KVBytes != 10*1000 {
		t.Errorf("KVBytes = %d, want 10000", res.KVBytes)
	}
	// total = (2000 + 10000) * 1.05 = 12600
	if res.TotalMemBytes != 12600 {
		t.Errorf("TotalMemBytes = %d, want 12600 (1.05 overhead)", res.TotalMemBytes)
	}
}

func TestAnnotateModels(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	machine := hardware.MachineInfo{GPUMemoryCapBytes: 100 * gb, SpeedConstant: 160}

	models := []manifest.Model{
		{
			ID:     "small",
			Params: "1.7B",
			Quantizations: []manifest.Quantization{
				{Label: "Q4_K_M", SizeBytes: 1 * gb},
			},
			ContextSize: 4096,
		},
		{
			ID:    "big-files-only",
			Files: []manifest.ModelFile{{Bytes: 50 * gb}},
		},
		{
			ID: "empty",
		},
	}

	got := AnnotateModels(models, machine, nil)
	if len(got) != 3 {
		t.Fatalf("AnnotateModels returned %d entries, want 3", len(got))
	}
	if got[0].Fit != FitPerfect {
		t.Errorf("small model fit = %s, want perfect", got[0].Fit)
	}
	// model with no quant and no files → tight.
	if got[2].Fit != FitTight {
		t.Errorf("empty model fit = %s, want tight", got[2].Fit)
	}
}

func TestAnnotateModelsContextOverride(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	// Small cap + large per-token KV so the context override visibly moves the
	// memory percentage rather than rounding to the same integer.
	machine := hardware.MachineInfo{GPUMemoryCapBytes: 8 * gb, SpeedConstant: 160}
	models := []manifest.Model{{
		ID:              "m",
		Params:          "4B",
		ContextSize:     4096,
		KVBytesPerToken: 200000,
		Quantizations:   []manifest.Quantization{{Label: "Q4_K_M", SizeBytes: 1 * gb}},
	}}

	base := AnnotateModels(models, machine, nil)
	overridden := AnnotateModels(models, machine, map[string]int{"m": 131072})

	// Larger context → larger memory footprint → higher (or equal) mem percent.
	if overridden[0].MemPercent <= base[0].MemPercent {
		t.Errorf("context override should raise mem percent: base %d, overridden %d",
			base[0].MemPercent, overridden[0].MemPercent)
	}

	// A zero/negative override is ignored.
	ignored := AnnotateModels(models, machine, map[string]int{"m": 0})
	if ignored[0].MemPercent != base[0].MemPercent {
		t.Errorf("zero override should be ignored: got %d, want %d",
			ignored[0].MemPercent, base[0].MemPercent)
	}
}

func TestSortByFit(t *testing.T) {
	models := []ModelFitInfo{
		{Model: manifest.Model{ID: "tight"}, Fit: FitTight, TPS: 100},
		{Model: manifest.Model{ID: "perfect-slow"}, Fit: FitPerfect, TPS: 10},
		{Model: manifest.Model{ID: "perfect-fast"}, Fit: FitPerfect, TPS: 50},
		{Model: manifest.Model{ID: "good"}, Fit: FitGood, TPS: 200},
	}
	SortByFit(models)

	wantOrder := []string{"perfect-fast", "perfect-slow", "good", "tight"}
	for i, want := range wantOrder {
		if models[i].ID != want {
			t.Errorf("position %d = %q, want %q", i, models[i].ID, want)
		}
	}
}

func TestSortByFitStableForEqualKeys(t *testing.T) {
	models := []ModelFitInfo{
		{Model: manifest.Model{ID: "a"}, Fit: FitGood, TPS: 50},
		{Model: manifest.Model{ID: "b"}, Fit: FitGood, TPS: 50},
		{Model: manifest.Model{ID: "c"}, Fit: FitGood, TPS: 50},
	}
	SortByFit(models)
	// Stable sort preserves original order for equal (fit, tps).
	for i, want := range []string{"a", "b", "c"} {
		if models[i].ID != want {
			t.Errorf("unstable sort: position %d = %q, want %q", i, models[i].ID, want)
		}
	}
}
