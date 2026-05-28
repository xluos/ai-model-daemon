package manifest

// ModelFile describes a single downloadable file within a model.
type ModelFile struct {
	Role     string   `json:"role"`
	Filename string   `json:"filename"`
	Bytes    int64    `json:"bytes"`
	URLs     []string `json:"urls"`
}

// Quantization describes a quantization variant of a model.
type Quantization struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Repo      string `json:"repo"`
	LLMFile   string `json:"llmFile"`
	MmprojFile string `json:"mmprojFile,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
}

// Model describes a known AI model that can be shared across apps.
type Model struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Desc    string      `json:"desc"`
	Files   []ModelFile `json:"files"`
	Enables []string    `json:"enables"`
	Apps    []string    `json:"apps"`
	Bundled bool        `json:"bundled"`

	// RuntimeKind declares which inference backend to use: "llm" (default), "whisper", or "" (no inference).
	RuntimeKind string `json:"runtimeKind,omitempty"`

	// Fields for fit calculation (GGUF models only).
	Family              string         `json:"family,omitempty"`
	Params              string         `json:"params,omitempty"`
	PrimaryCapabilities []string       `json:"primaryCapabilities,omitempty"`
	SecondaryTags       []string       `json:"secondaryTags,omitempty"`
	ContextSize         int            `json:"contextSize,omitempty"`
	NativeContextSize   int            `json:"nativeContextSize,omitempty"`
	KVBytesPerToken     int            `json:"kvBytesPerToken,omitempty"`
	IsThinking          bool           `json:"isThinking,omitempty"`
	Available           *bool          `json:"available,omitempty"`
	Quantizations       []Quantization `json:"quantizations,omitempty"`
}

func (m *Model) TotalBytes() int64 {
	var total int64
	for _, f := range m.Files {
		total += f.Bytes
	}
	return total
}

func (m *Model) FindFile(role string) *ModelFile {
	for i := range m.Files {
		if m.Files[i].Role == role {
			return &m.Files[i]
		}
	}
	return nil
}

func boolPtr(v bool) *bool { return &v }

func hfURLs(repo, file string) []string {
	return []string{
		"https://hf-mirror.com/" + repo + "/resolve/main/" + file,
		"https://huggingface.co/" + repo + "/resolve/main/" + file,
	}
}

func vlmFiles(repo, llmFile string, llmBytes int64, mmprojFile string, mmprojBytes int64) []ModelFile {
	return []ModelFile{
		{Role: "llm", Filename: llmFile, Bytes: llmBytes, URLs: hfURLs(repo, llmFile)},
		{Role: "mmproj", Filename: mmprojFile, Bytes: mmprojBytes, URLs: hfURLs(repo, mmprojFile)},
	}
}

// Registry contains all known models across all client apps.
var Registry = []Model{
	// --- flashcull: single-file ONNX models ---
	{
		ID:      "yunet",
		Name:    "YuNet Face Detection",
		Desc:    "Lightweight face detection model from OpenCV Zoo",
		Files:   []ModelFile{{Filename: "face_detection_yunet_2023mar.onnx", Bytes: 232589, URLs: []string{"https://github.com/opencv/opencv_zoo/raw/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx"}}},
		Apps:    []string{"flashcull"},
		Bundled: true,
	},
	{
		ID:   "dinov2-small",
		Name: "DINOv2-small (384D)",
		Desc: "Self-supervised vision transformer for semantic embeddings",
		Files: []ModelFile{{
			Filename: "dinov2_small.onnx",
			Bytes:    86_000_000,
			URLs:     hfURLs("onnx-community/dinov2-small", "onnx/model.onnx"),
		}},
		Apps:    []string{"flashcull"},
		Enables: []string{"semantic_cluster"},
	},
	{
		ID:   "nima",
		Name: "NIMA Aesthetic",
		Desc: "Neural Image Assessment for aesthetic quality scoring",
		Files: []ModelFile{{
			Filename: "nima_mobilenet.onnx",
			Bytes:    14_000_000,
			URLs:     hfURLs("flashcull/nima-mobilenet", "nima_mobilenet.onnx"),
		}},
		Apps:    []string{"flashcull"},
		Enables: []string{"aesthetic"},
	},
	{
		ID:   "insightface",
		Name: "InsightFace Buffalo",
		Desc: "Face recognition and embedding model for clustering",
		Files: []ModelFile{{
			Filename: "insightface_buffalo.onnx",
			Bytes:    130_000_000,
			URLs:     hfURLs("flashcull/insightface-buffalo", "insightface_buffalo.onnx"),
		}},
		Apps:    []string{"flashcull"},
		Enables: []string{"face_id", "face_cluster"},
	},
	// --- shared: text-only LLM ---
	{
		ID:     "qwen3-4b-q4",
		Name:   "Qwen3-4B Q4_K_M",
		Desc:   "Quantized Qwen3 4B for local LLM inference",
		Family: "Qwen3",
		Params: "4B",
		Files: []ModelFile{{
			Role:     "llm",
			Filename: "Qwen3-4B-Q4_K_M.gguf",
			Bytes:    2_800_000_000,
			URLs:     hfURLs("unsloth/Qwen3-4B-GGUF", "Qwen3-4B-Q4_K_M.gguf"),
		}},
		Apps:                []string{"clipiq", "flashcull"},
		Enables:             []string{"local_llm"},
		PrimaryCapabilities: []string{"text"},
		ContextSize:         32768,
		NativeContextSize:   131072,
		Quantizations:       []Quantization{{Key: "q4_k_m", Label: "Q4_K_M", Repo: "unsloth/Qwen3-4B-GGUF", LLMFile: "Qwen3-4B-Q4_K_M.gguf", SizeBytes: 2_800_000_000}},
	},
	// --- clipiq: Qwen3.5 VLM series ---
	{
		ID:                  "qwen3_5_0_8b_q4km",
		Name:                "Qwen3.5 0.8B Q4_K_M",
		Desc:                "Fastest tier, bulk frame pre-screening",
		Family:              "Qwen3.5",
		Params:              "0.8B",
		Files:               vlmFiles("unsloth/Qwen3.5-0.8B-GGUF", "Qwen3.5-0.8B-Q4_K_M.gguf", 0, "mmproj-F16.gguf", 0),
		Apps:                []string{"clipiq"},
		Enables:             []string{"local_vlm"},
		PrimaryCapabilities: []string{"vision", "text"},
		SecondaryTags:       []string{"fast"},
		Available:           boolPtr(true),
		IsThinking:          true,
		NativeContextSize:   262144,
		ContextSize:         8192,
		Quantizations:       []Quantization{{Key: "q4_k_m", Label: "Q4_K_M", Repo: "unsloth/Qwen3.5-0.8B-GGUF", LLMFile: "Qwen3.5-0.8B-Q4_K_M.gguf", MmprojFile: "mmproj-F16.gguf", SizeBytes: 740 * 1024 * 1024}},
	},
	{
		ID:                  "qwen3_5_2b_q4km",
		Name:                "Qwen3.5 2B Q4_K_M",
		Desc:                "Balanced CJK recognition, recommended default",
		Family:              "Qwen3.5",
		Params:              "2B",
		Files:               vlmFiles("unsloth/Qwen3.5-2B-GGUF", "Qwen3.5-2B-Q4_K_M.gguf", 0, "mmproj-F16.gguf", 0),
		Apps:                []string{"clipiq"},
		Enables:             []string{"local_vlm"},
		PrimaryCapabilities: []string{"vision", "text"},
		SecondaryTags:       []string{"chinese"},
		Available:           boolPtr(true),
		IsThinking:          true,
		NativeContextSize:   262144,
		ContextSize:         8192,
		Quantizations:       []Quantization{{Key: "q4_k_m", Label: "Q4_K_M", Repo: "unsloth/Qwen3.5-2B-GGUF", LLMFile: "Qwen3.5-2B-Q4_K_M.gguf", MmprojFile: "mmproj-F16.gguf", SizeBytes: 1860 * 1024 * 1024}},
	},
	{
		ID:                  "qwen3_5_4b_q4km",
		Name:                "Qwen3.5 4B Q4_K_M",
		Desc:                "Higher quality, 16 GB machine primary tier",
		Family:              "Qwen3.5",
		Params:              "4B",
		Files:               vlmFiles("unsloth/Qwen3.5-4B-GGUF", "Qwen3.5-4B-Q4_K_M.gguf", 0, "mmproj-F16.gguf", 0),
		Apps:                []string{"clipiq"},
		Enables:             []string{"local_vlm"},
		PrimaryCapabilities: []string{"vision", "text"},
		SecondaryTags:       []string{"chinese"},
		Available:           boolPtr(true),
		IsThinking:          true,
		NativeContextSize:   262144,
		ContextSize:         16384,
		Quantizations:       []Quantization{{Key: "q4_k_m", Label: "Q4_K_M", Repo: "unsloth/Qwen3.5-4B-GGUF", LLMFile: "Qwen3.5-4B-Q4_K_M.gguf", MmprojFile: "mmproj-F16.gguf", SizeBytes: 3250 * 1024 * 1024}},
	},
	{
		ID:                  "qwen3_5_9b_q4km",
		Name:                "Qwen3.5 9B Q4_K_M",
		Desc:                "Higher fidelity, requires 24 GB+ machine",
		Family:              "Qwen3.5",
		Params:              "9B",
		Files:               vlmFiles("unsloth/Qwen3.5-9B-GGUF", "Qwen3.5-9B-Q4_K_M.gguf", 0, "mmproj-F16.gguf", 0),
		Apps:                []string{"clipiq"},
		Enables:             []string{"local_vlm"},
		PrimaryCapabilities: []string{"vision", "text"},
		SecondaryTags:       []string{"chinese", "reasoning"},
		Available:           boolPtr(true),
		IsThinking:          true,
		NativeContextSize:   262144,
		ContextSize:         32768,
		Quantizations:       []Quantization{{Key: "q4_k_m", Label: "Q4_K_M", Repo: "unsloth/Qwen3.5-9B-GGUF", LLMFile: "Qwen3.5-9B-Q4_K_M.gguf", MmprojFile: "mmproj-F16.gguf", SizeBytes: 5_800_000_000}},
	},
	{
		ID:                  "qwen3_5_27b_q4km",
		Name:                "Qwen3.5 27B Q4_K_M",
		Desc:                "High quality tier, requires 32 GB+ machine",
		Family:              "Qwen3.5",
		Params:              "27B",
		Files:               vlmFiles("unsloth/Qwen3.5-27B-GGUF", "Qwen3.5-27B-Q4_K_M.gguf", 0, "mmproj-F16.gguf", 0),
		Apps:                []string{"clipiq"},
		Enables:             []string{"local_vlm"},
		PrimaryCapabilities: []string{"vision", "text"},
		SecondaryTags:       []string{"chinese", "reasoning"},
		Available:           boolPtr(true),
		IsThinking:          true,
		NativeContextSize:   262144,
		ContextSize:         32768,
		Quantizations:       []Quantization{{Key: "q4_k_m", Label: "Q4_K_M", Repo: "unsloth/Qwen3.5-27B-GGUF", LLMFile: "Qwen3.5-27B-Q4_K_M.gguf", MmprojFile: "mmproj-F16.gguf", SizeBytes: 17 * 1024 * 1024 * 1024}},
	},
	{
		ID:                  "qwen3_5_35b_a3b_udq4xl",
		Name:                "Qwen3.5 35B-A3B UD-Q4_K_XL",
		Desc:                "MoE 3B active, near-4B speed, requires 32 GB+",
		Family:              "Qwen3.5",
		Params:              "35B-A3B",
		Files:               vlmFiles("unsloth/Qwen3.5-35B-A3B-GGUF", "Qwen3.5-35B-A3B-UD-Q4_K_XL.gguf", 0, "mmproj-F16.gguf", 0),
		Apps:                []string{"clipiq"},
		Enables:             []string{"local_vlm"},
		PrimaryCapabilities: []string{"vision", "text"},
		SecondaryTags:       []string{"chinese", "reasoning", "moe"},
		Available:           boolPtr(true),
		IsThinking:          true,
		NativeContextSize:   262144,
		ContextSize:         32768,
		Quantizations:       []Quantization{{Key: "ud_q4_k_xl", Label: "UD-Q4_K_XL", Repo: "unsloth/Qwen3.5-35B-A3B-GGUF", LLMFile: "Qwen3.5-35B-A3B-UD-Q4_K_XL.gguf", MmprojFile: "mmproj-F16.gguf", SizeBytes: 21 * 1024 * 1024 * 1024}},
	},
	// --- clipiq: Qwen3.6 VLM series ---
	{
		ID:                  "qwen3_6_27b_q4km",
		Name:                "Qwen3.6 27B Q4_K_M",
		Desc:                "27B upgrade, native 256k context, requires 32 GB+",
		Family:              "Qwen3.6",
		Params:              "27B",
		Files:               vlmFiles("unsloth/Qwen3.6-27B-GGUF", "Qwen3.6-27B-Q4_K_M.gguf", 0, "mmproj-F16.gguf", 0),
		Apps:                []string{"clipiq"},
		Enables:             []string{"local_vlm"},
		PrimaryCapabilities: []string{"vision", "text"},
		SecondaryTags:       []string{"chinese", "reasoning", "long_context"},
		Available:           boolPtr(true),
		IsThinking:          true,
		NativeContextSize:   262144,
		ContextSize:         65536,
		Quantizations:       []Quantization{{Key: "q4_k_m", Label: "Q4_K_M", Repo: "unsloth/Qwen3.6-27B-GGUF", LLMFile: "Qwen3.6-27B-Q4_K_M.gguf", MmprojFile: "mmproj-F16.gguf", SizeBytes: 17 * 1024 * 1024 * 1024}},
	},
	{
		ID:                  "qwen3_6_35b_a3b_udq4xl",
		Name:                "Qwen3.6 35B-A3B UD-Q4_K_XL",
		Desc:                "MoE 3B active, native 256k context, requires 32 GB+",
		Family:              "Qwen3.6",
		Params:              "35B-A3B",
		Files:               vlmFiles("unsloth/Qwen3.6-35B-A3B-GGUF", "Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf", 0, "mmproj-F16.gguf", 0),
		Apps:                []string{"clipiq"},
		Enables:             []string{"local_vlm"},
		PrimaryCapabilities: []string{"vision", "text"},
		SecondaryTags:       []string{"chinese", "reasoning", "moe", "long_context"},
		Available:           boolPtr(true),
		IsThinking:          true,
		NativeContextSize:   262144,
		ContextSize:         65536,
		Quantizations:       []Quantization{{Key: "ud_q4_k_xl", Label: "UD-Q4_K_XL", Repo: "unsloth/Qwen3.6-35B-A3B-GGUF", LLMFile: "Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf", MmprojFile: "mmproj-F16.gguf", SizeBytes: 21 * 1024 * 1024 * 1024}},
	},

	// --- Whisper ASR models ---
	{
		ID:          "whisper-tiny",
		Name:        "Whisper Tiny",
		Desc:        "Fastest, lowest accuracy (75 MB)",
		RuntimeKind: "whisper",
		Files:       []ModelFile{{Role: "model", Filename: "ggml-tiny.bin", Bytes: 75 * 1024 * 1024, URLs: hfURLs("ggerganov/whisper.cpp", "ggml-tiny.bin")}},
		Apps:        []string{"clipiq"},
		Enables:     []string{"transcription"},
	},
	{
		ID:          "whisper-base",
		Name:        "Whisper Base",
		Desc:        "Fast with decent accuracy (142 MB)",
		RuntimeKind: "whisper",
		Files:       []ModelFile{{Role: "model", Filename: "ggml-base.bin", Bytes: 142 * 1024 * 1024, URLs: hfURLs("ggerganov/whisper.cpp", "ggml-base.bin")}},
		Apps:        []string{"clipiq"},
		Enables:     []string{"transcription"},
	},
	{
		ID:          "whisper-small",
		Name:        "Whisper Small",
		Desc:        "Balanced speed and accuracy (466 MB)",
		RuntimeKind: "whisper",
		Files:       []ModelFile{{Role: "model", Filename: "ggml-small.bin", Bytes: 466 * 1024 * 1024, URLs: hfURLs("ggerganov/whisper.cpp", "ggml-small.bin")}},
		Apps:        []string{"clipiq"},
		Enables:     []string{"transcription"},
	},
	{
		ID:          "whisper-medium",
		Name:        "Whisper Medium",
		Desc:        "High accuracy (1.5 GB)",
		RuntimeKind: "whisper",
		Files:       []ModelFile{{Role: "model", Filename: "ggml-medium.bin", Bytes: 1536 * 1024 * 1024, URLs: hfURLs("ggerganov/whisper.cpp", "ggml-medium.bin")}},
		Apps:        []string{"clipiq"},
		Enables:     []string{"transcription"},
	},
	{
		ID:          "whisper-large-v3-turbo",
		Name:        "Whisper Large V3 Turbo",
		Desc:        "Best accuracy, Turbo variant (~1.6 GB)",
		RuntimeKind: "whisper",
		Files:       []ModelFile{{Role: "model", Filename: "ggml-large-v3-turbo.bin", Bytes: 1600 * 1024 * 1024, URLs: hfURLs("ggerganov/whisper.cpp", "ggml-large-v3-turbo.bin")}},
		Apps:        []string{"clipiq"},
		Enables:     []string{"transcription"},
	},
}

// Find returns a model by ID, or nil if not found.
func Find(id string) *Model {
	for i := range Registry {
		if Registry[i].ID == id {
			return &Registry[i]
		}
	}
	return nil
}
