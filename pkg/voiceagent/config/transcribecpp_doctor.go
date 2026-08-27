package config

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// transcribeCLICheck is one transcribe.cpp CLI plus the weights it loads. The
// optional STT provider and the optional Sortformer review diarizer share the
// binary, the pin and the backend probe; only the weights and the GPU rule
// differ, so both resolve through diagnoseTranscribeCLI.
type transcribeCLICheck struct {
	name string
	// resolve finds the CLI: explicit config path, then PATH, then the
	// managed pack models ensure installs (ResolveTranscribeCPPBinary).
	resolve func() (string, error)
	// model resolves the weights path. It is a function so it runs after the
	// binary check and a machine missing the CLI is told that first.
	model            func() (string, error)
	modelRemediation string
	missingRemedy    string
	// requireGPU fails a machine with no GPU lane. STT sets it — transcribe.cpp
	// on CPU loses to the sherpa-parakeet default it replaced (T1) — while
	// diarization does not: Sortformer on CPU is still 2.6x the sherpa review
	// path it replaces (T3).
	requireGPU bool
}

// diagnoseTranscribeCLI reports on the two things an optional transcribe.cpp
// path needs before it can run: the external CLI and its managed GGUF. Both are
// construction-time failures, so a missing one is an error here rather than the
// warning a merely-undownloaded asset gets.
func diagnoseTranscribeCLI(c transcribeCLICheck, lookPath func(string) (string, error)) Diagnostic {
	binPath, err := c.resolve()
	if err != nil {
		return Diagnostic{
			Name:        c.name,
			Severity:    SeverityError,
			Detail:      err.Error(),
			Remediation: TranscribeCPPCLIRemediation(),
		}
	}

	modelPath, err := c.model()
	if err != nil {
		return Diagnostic{
			Name:        c.name,
			Severity:    SeverityError,
			Detail:      err.Error(),
			Remediation: c.modelRemediation,
		}
	}
	if _, err := os.Stat(modelPath); err != nil {
		return Diagnostic{
			Name:        c.name,
			Severity:    SeverityError,
			Detail:      fmt.Sprintf("%s found, but the GGUF is missing at %s", binPath, modelPath),
			Remediation: c.missingRemedy,
		}
	}

	// Mirror the caller's fail-closed backend check, so doctor reports the same
	// refusal construction would raise instead of a clean bill of health.
	lanes, err := TranscribeCPPLanes(context.Background(), binPath)
	if err != nil {
		return Diagnostic{
			Name:        c.name,
			Severity:    SeverityError,
			Detail:      err.Error(),
			Remediation: "check that " + binPath + " runs and supports --list-devices (transcribe.cpp v0.2.2 or newer)",
		}
	}
	detail := fmt.Sprintf("%s + %s (backends: %s)", binPath, modelPath, strings.Join(LaneKinds(lanes), ", "))
	if c.requireGPU && len(GPULanes(lanes)) == 0 {
		return Diagnostic{
			Name:        c.name,
			Severity:    SeverityError,
			Detail:      detail + " — no GPU lane; transcribe.cpp on CPU is slower than the default sherpa-parakeet",
			Remediation: "use a machine with a Metal or CUDA lane, or set stt_provider back to sherpa-parakeet",
		}
	}
	return Diagnostic{Name: c.name, Severity: SeverityOK, Detail: detail}
}

// diagnoseTranscribeCPP is the STT provider's view of that check.
func diagnoseTranscribeCPP(cfg *Config, modelsDir string, lookPath func(string) (string, error)) Diagnostic {
	return diagnoseTranscribeCLI(transcribeCLICheck{
		name:    "transcribecpp-binary",
		resolve: func() (string, error) { return ResolveTranscribeCPPBinary(cfg, modelsDir, lookPath) },
		model: func() (string, error) {
			asset, err := TranscribeCPPModel(cfg.TranscribeCPPModel)
			if err != nil {
				return "", err
			}
			return asset.ModelPath(modelsDir), nil
		},
		modelRemediation: "set transcribecpp_model to " + strings.Join(TranscribeCPPModelNames(), " or "),
		missingRemedy:    "run 'obey-voice models ensure' to download the transcribe.cpp weights",
		requireGPU:       true,
	}, lookPath)
}
