package fit

import (
	"math"
	"regexp"
	"sort"
	"strconv"

	"github.com/xushuaiwu/ai-model-daemon/internal/hardware"
	"github.com/xushuaiwu/ai-model-daemon/internal/manifest"
)

type FitLevel string

const (
	FitPerfect  FitLevel = "perfect"
	FitGood     FitLevel = "good"
	FitMarginal FitLevel = "marginal"
	FitTight    FitLevel = "tight"
)

type QuantFitResult struct {
	Fit          FitLevel `json:"fit"`
	MemPercent   int      `json:"memPercent"`
	TPS          int      `json:"tps"`
	TotalMemBytes int64   `json:"totalMemBytes"`
	WeightBytes  int64    `json:"weightBytes"`
	KVBytes      int64    `json:"kvBytes"`
	MemCapBytes  int64    `json:"memCapBytes"`
	EffectiveCtx int      `json:"effectiveCtx"`
}

type ModelFitInfo struct {
	manifest.Model
	Fit        FitLevel `json:"fit"`
	MemPercent int      `json:"memPercent"`
	TPS        int      `json:"tps"`
}

// Qwen3 series KV cache bytes per token (F16, empirical).
// Formula: 2(K+V) × num_layers × num_kv_heads × head_dim × 2(F16)
var qwen3KVBytesPerToken = map[float64]int{
	0.6: 12 * 1024,
	0.8: 14 * 1024,
	1.7: 28 * 1024,
	2:   30 * 1024,
	4:   60 * 1024,
	7:   130 * 1024,
	8:   140 * 1024,
	9:   144 * 1024,
	14:  180 * 1024,
	27:  220 * 1024,
	30:  230 * 1024,
	32:  240 * 1024,
	35:  280 * 1024,
}

var quantSpeedMult = map[string]float64{
	"Q2_K":   1.25,
	"Q3_K_M": 1.15,
	"Q4_K_M": 1.0,
	"Q5_K_M": 0.85,
	"Q6_K":   0.75,
	"Q8_0":   0.6,
}

var paramsRegexp = regexp.MustCompile(`([\d.]+)\s*B`)

func ParseParams(s string) float64 {
	m := paramsRegexp.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	return v
}

func estimateKVBytesPerToken(entry *manifest.Model, paramsB float64) int {
	if entry != nil && entry.KVBytesPerToken > 0 {
		return entry.KVBytesPerToken
	}
	if paramsB > 0 {
		keys := make([]float64, 0, len(qwen3KVBytesPerToken))
		for k := range qwen3KVBytesPerToken {
			keys = append(keys, k)
		}
		sort.Float64s(keys)

		nearest := keys[0]
		bestDiff := math.Abs(paramsB - nearest)
		for _, k := range keys {
			d := math.Abs(paramsB - k)
			if d < bestDiff {
				bestDiff = d
				nearest = k
			}
		}
		return qwen3KVBytesPerToken[nearest]
	}
	return 64 * 1024
}

func ComputeQuantFit(
	quantSizeBytes int64,
	quantLabel string,
	machine hardware.MachineInfo,
	paramsB float64,
	effectiveCtx int,
	entry *manifest.Model,
) QuantFitResult {
	ctx := effectiveCtx
	if ctx <= 0 {
		if entry != nil && entry.ContextSize > 0 {
			ctx = entry.ContextSize
		} else {
			ctx = 8192
		}
	}

	kvPerToken := estimateKVBytesPerToken(entry, paramsB)
	kvBytes := int64(ctx) * int64(kvPerToken)
	totalMem := quantSizeBytes + kvBytes
	totalWithOverhead := int64(math.Round(float64(totalMem) * 1.05))

	memCap := machine.GPUMemoryCapBytes
	if memCap <= 0 {
		memCap = machine.AvailableMemoryBytes
	}

	var fit FitLevel
	if memCap <= 0 {
		fit = FitTight
	} else {
		ratio := float64(totalWithOverhead) / float64(memCap)
		switch {
		case ratio < 0.5:
			fit = FitPerfect
		case ratio < 0.75:
			fit = FitGood
		case ratio < 0.95:
			fit = FitMarginal
		default:
			fit = FitTight
		}
	}

	memPercent := 0
	if memCap > 0 {
		memPercent = int(math.Round(float64(totalWithOverhead) / float64(memCap) * 100))
	}

	speedMult := 1.0
	if m, ok := quantSpeedMult[quantLabel]; ok {
		speedMult = m
	}
	tps := 0
	if paramsB > 0 {
		tps = int(math.Round(float64(machine.SpeedConstant) / paramsB * speedMult))
	}

	return QuantFitResult{
		Fit:           fit,
		MemPercent:    memPercent,
		TPS:           tps,
		TotalMemBytes: totalWithOverhead,
		WeightBytes:   quantSizeBytes,
		KVBytes:       kvBytes,
		MemCapBytes:   memCap,
		EffectiveCtx:  ctx,
	}
}

// FitRank returns a sort key (lower = better fit).
func FitRank(f FitLevel) int {
	switch f {
	case FitPerfect:
		return 0
	case FitGood:
		return 1
	case FitMarginal:
		return 2
	case FitTight:
		return 3
	default:
		return 4
	}
}

// AnnotateModels computes fit/memPercent/tps for each model in the registry.
// ctxOverrides maps model ID → user-configured context size.
func AnnotateModels(models []manifest.Model, machine hardware.MachineInfo, ctxOverrides map[string]int) []ModelFitInfo {
	results := make([]ModelFitInfo, 0, len(models))
	for i := range models {
		m := &models[i]
		paramsB := ParseParams(m.Params)
		effectiveCtx := m.ContextSize
		if override, ok := ctxOverrides[m.ID]; ok && override > 0 {
			effectiveCtx = override
		}

		var fitResult QuantFitResult
		if len(m.Quantizations) > 0 {
			q := m.Quantizations[0]
			fitResult = ComputeQuantFit(q.SizeBytes, q.Label, machine, paramsB, effectiveCtx, m)
		} else if m.TotalBytes() > 0 {
			fitResult = ComputeQuantFit(m.TotalBytes(), "", machine, paramsB, effectiveCtx, m)
		} else {
			fitResult = QuantFitResult{Fit: FitTight}
		}

		results = append(results, ModelFitInfo{
			Model:      *m,
			Fit:        fitResult.Fit,
			MemPercent: fitResult.MemPercent,
			TPS:        fitResult.TPS,
		})
	}
	return results
}

// SortByFit sorts models by fit level (best first), then by TPS (highest first).
func SortByFit(models []ModelFitInfo) {
	sort.SliceStable(models, func(i, j int) bool {
		ri, rj := FitRank(models[i].Fit), FitRank(models[j].Fit)
		if ri != rj {
			return ri < rj
		}
		return models[i].TPS > models[j].TPS
	})
}
