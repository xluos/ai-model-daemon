//go:build windows

package runtime

func (bm *BinaryManager) platformAsset(kind BinaryKind) *binaryAsset {
	key := platformKey()
	switch kind {
	case BinaryLlamaServer:
		switch key {
		case "windows-arm64":
			return &binaryAsset{
				Name:   llamaCppAssetName("llama-${REL}-bin-win-cpu-arm64.zip"),
				Format: "zip",
			}
		case "windows-x64":
			return &binaryAsset{
				Name:   llamaCppAssetName("llama-${REL}-bin-win-cpu-x64.zip"),
				Format: "zip",
			}
		}
	case BinaryWhisperServer:
		switch key {
		case "windows-x64":
			return &binaryAsset{
				Name:   "whisper-bin-x64.zip",
				Format: "zip",
			}
		}
	}
	return nil
}
