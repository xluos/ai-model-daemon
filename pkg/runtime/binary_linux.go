//go:build linux

package runtime

func (bm *BinaryManager) platformAsset(kind BinaryKind) *binaryAsset {
	key := platformKey()
	switch kind {
	case BinaryLlamaServer:
		switch key {
		case "linux-arm64":
			return &binaryAsset{
				Name:   llamaCppAssetName("llama-${REL}-bin-ubuntu-arm64.tar.gz"),
				Format: "tar.gz",
			}
		case "linux-x64":
			return &binaryAsset{
				Name:   llamaCppAssetName("llama-${REL}-bin-ubuntu-x64.tar.gz"),
				Format: "tar.gz",
			}
		}
	case BinaryWhisperServer:
		return nil // Linux: 走源码编译 (buildFromSource)
	}
	return nil
}
