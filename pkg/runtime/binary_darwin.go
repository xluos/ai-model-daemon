//go:build darwin

package runtime

func (bm *BinaryManager) platformAsset(kind BinaryKind) *binaryAsset {
	key := platformKey()
	switch kind {
	case BinaryLlamaServer:
		switch key {
		case "darwin-arm64":
			return &binaryAsset{
				Name:   llamaCppAssetName("llama-${REL}-bin-macos-arm64.tar.gz"),
				Format: "tar.gz",
			}
		case "darwin-x64":
			return &binaryAsset{
				Name:   llamaCppAssetName("llama-${REL}-bin-macos-x64.tar.gz"),
				Format: "tar.gz",
			}
		}
	case BinaryWhisperServer:
		return nil // macOS: 走源码编译 (buildFromSource)
	}
	return nil
}
