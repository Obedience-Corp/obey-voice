package config

import (
	"fmt"
	"os"
	"strings"
)

// STTGap is something the configured recognizer needs and this computer does
// not have. It is an environment problem, not a configuration one: the setting
// is valid and worth saving, it just cannot run here yet.
type STTGap struct {
	Detail      string
	Remediation string
}

// Missing reports whether anything is actually missing.
func (g STTGap) Missing() bool { return g.Detail != "" }

// STTEnvironmentGap reports what the named provider is missing on this
// computer. It is deliberately cheap — a PATH lookup and a file stat, no
// subprocess — so a picker can call it while building rows. doctor's
// transcribecpp row runs the deeper backend probe.
//
// provider is a normalized name (STTProvider*). Managed sherpa/parakeet assets
// are downloaded on demand at conversation start, so they report no gap.
func STTEnvironmentGap(provider string, cfg *Config, modelsDir string, lookPath func(string) (string, error)) STTGap {
	if lookPath == nil || cfg == nil {
		return STTGap{}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case STTProviderWhisperCPP:
		binary := strings.TrimSpace(cfg.WhisperCPPBinary)
		if binary == "" {
			binary = "whisper-cli"
		}
		if _, err := lookPath(binary); err != nil {
			return STTGap{
				Detail:      fmt.Sprintf("%s is not on PATH", binary),
				Remediation: "install whisper.cpp, or set whispercpp_binary to its path",
			}
		}
		return STTGap{}

	case STTProviderTranscribeCPP:
		if _, err := ResolveTranscribeCPPBinary(cfg, modelsDir, lookPath); err != nil {
			return STTGap{
				Detail:      err.Error(),
				Remediation: TranscribeCPPCLIRemediation(),
			}
		}
		asset, err := TranscribeCPPModel(cfg.TranscribeCPPModel)
		if err != nil {
			return STTGap{
				Detail:      err.Error(),
				Remediation: "set transcribecpp_model to " + strings.Join(TranscribeCPPModelNames(), " or "),
			}
		}
		modelPath := asset.ModelPath(modelsDir)
		if _, err := os.Stat(modelPath); err != nil {
			return STTGap{
				Detail:      "the weights are not downloaded yet",
				Remediation: "run 'obey-voice models ensure'",
			}
		}
		return STTGap{}
	}
	return STTGap{}
}
