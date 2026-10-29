package config

import "fmt"

const AcousticNameModelDir = "sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01"

// AcousticNameAsset is the optional English keyword model. The archive's
// packaged README declares Apache-2.0; the checksum pins the released bytes.
func AcousticNameAsset() Asset {
	return Asset{
		ID: "stt.kws.obey.v1", Provider: "sherpa", Kind: AssetKindSTT,
		Name: "Obey acoustic name detector (experimental)", TargetDir: AcousticNameModelDir,
		Archive: &AssetArchive{
			URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/kws-models/" + AcousticNameModelDir + ".tar.bz2",
			SHA256: "f170013b4716e41b62b9bfd809687c207cef798ef9bc6534d524e17af9b6561a",
		},
		CheckFiles: []string{
			"encoder-epoch-12-avg-2-chunk-16-left-64.onnx",
			"decoder-epoch-12-avg-2-chunk-16-left-64.onnx",
			"joiner-epoch-12-avg-2-chunk-16-left-64.onnx", "tokens.txt",
		},
		License: "Apache-2.0",
		Source:  "https://www.modelscope.cn/pkufool/" + AcousticNameModelDir,
	}
}

// ValidateAcousticName rejects configurations that would silently enable an
// experiment without running its detector.
func ValidateAcousticName(cfg *Config) error {
	if cfg == nil || !cfg.AcousticNameEnabled {
		return nil
	}
	norm, err := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil || norm.Provider != STTProviderParakeet {
		return fmt.Errorf("acoustic_name_enabled requires stt_provider=parakeet")
	}
	return nil
}
