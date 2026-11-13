package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/calibre"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

var (
	doctorJSON         bool
	doctorVoiceDevices bool
)

// voiceDeviceProbeTimeout bounds the opt-in hardware probe so a wedged audio
// backend cannot hang doctor.
const voiceDeviceProbeTimeout = 3 * time.Second

// newVoiceDeviceChecker is set by doctor_voice.go (!integration) to the real
// audio.DeviceChecker. The integration Linux binary leaves this nil so it can
// build with CGO_ENABLED=0 (no sherpa).
var newVoiceDeviceChecker func() config.VoiceDeviceChecker

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose local setup: config, model assets, and external binaries (read-only)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		var checker config.VoiceDeviceChecker
		if doctorVoiceDevices {
			if newVoiceDeviceChecker == nil {
				return fmt.Errorf("doctor --voice-devices is not available in this build")
			}
			checker = newVoiceDeviceChecker()
		}
		return runDoctor(cmd, cfg, config.ModelsDir(), doctorLookPath, checker, nil, doctorJSON)
	},
}

// doctorLookPath resolves external tools for doctor. calibredb/ebook-convert use
// Calibre's bundle-aware lookup; everything else uses PATH.
func doctorLookPath(name string) (string, error) {
	switch name {
	case "calibredb", "ebook-convert", "ebook-meta":
		return calibre.BundleLookPath(name)
	default:
		return exec.LookPath(name)
	}
}

var severityMark = map[config.Severity]string{
	config.SeverityOK:    "OK  ",
	config.SeverityWarn:  "WARN",
	config.SeverityError: "FAIL",
}

// runDoctor prints read-only setup diagnostics and returns an error (non-zero
// exit) only when a check has error severity. lookPath is injectable for
// tests. voiceChecker is nil unless --voice-devices opted in to hardware
// probes (inject a fake in tests). speechProbe is nil in production, which
// uses the real obey-speech/v1 probe.
func runDoctor(cmd *cobra.Command, cfg *config.Config, modelsDir string, lookPath func(string) (string, error), voiceChecker config.VoiceDeviceChecker, speechProbe config.SpeechProbe, asJSON bool) error {
	diags := config.Diagnose(cfg, modelsDir, lookPath)
	diags = append(diags, personaQwenDiags(cfg, modelsDir)...)
	diags = append(diags, personaWorkDiags(cfg)...)
	diags = append(diags, bargeInSpeakersDiags(cfg)...)
	diags = append(diags, speakersEnrollmentDiags(cfg)...)

	// Where speech runs, and whether a LAN speech worker answers. The probe
	// bounds itself with tts_lan_timeout_ms; local never dials.
	speechCtx := cmd.Context()
	if speechCtx == nil {
		speechCtx = context.Background()
	}
	// Whether the system-wide talk key can be held right now: a real
	// registration, released at once.
	diags = append(diags, talkHotkeyDiags(speechCtx, cfg)...)
	diags = append(diags, config.DiagnoseSpeechPlace(speechCtx, cfg, speechProbe)...)
	// openai-speech asks its server what it serves, bounded to a few seconds.
	diags = append(diags, config.DiagnoseOpenAISpeech(speechCtx, cfg, modelsDir)...)
	// Whether that speech is played chunk by chunk. Read-only: it reports the
	// policy, and never starts a worker to ask.
	diags = append(diags, config.DiagnoseQwenStreaming(cfg, modelsDir)...)
	// And how many engines produce it. Read-only for the same reason: the row
	// reports the configured pool, never a started worker.
	diags = append(diags, config.DiagnoseQwenWorkers(cfg, modelsDir)...)

	if voiceChecker != nil {
		parent := cmd.Context()
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, voiceDeviceProbeTimeout)
		defer cancel()
		diags = append(diags, config.DiagnoseVoiceDevices(ctx, voiceChecker)...)
	}

	out := cmd.OutOrStdout()

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(diags); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "\n  Obey Voice Doctor (models dir: %s)\n\n", modelsDir)
		for _, d := range diags {
			fmt.Fprintf(out, "  [%s] %s: %s\n", severityMark[d.Severity], d.Name, d.Detail)
			if d.Remediation != "" {
				fmt.Fprintf(out, "         → %s\n", d.Remediation)
			}
		}
		fmt.Fprintln(out)
	}

	if config.HasErrors(diags) {
		return fmt.Errorf("doctor found setup errors; see remediation above")
	}
	return nil
}

// personaQwenDiags reports personas whose speech cannot start: they route
// through qwen3-tts but the installed native package lacks their tier (or is
// absent entirely). The active provider alone cannot see this — a kokoro
// session with a qwen persona still needs the package.
func personaQwenDiags(cfg *config.Config, modelsDir string) []config.Diagnostic {
	profiles, err := persona.List()
	if err != nil {
		return nil
	}
	var diags []config.Diagnostic
	for _, p := range profiles {
		if p == nil || !strings.EqualFold(strings.TrimSpace(p.TTS.Provider), managedqwen.ProviderName) {
			continue
		}
		tier := strings.TrimSpace(p.TTS.Tier)
		if tier == "" && cfg != nil {
			tier = strings.TrimSpace(cfg.QwenTTSModelTier)
		}
		tier = managedqwen.NormalizeModelTier(tier)
		st := managedqwen.InspectNative(modelsDir, tier)
		if st.Installed && st.ModelReady {
			continue
		}
		detail := fmt.Sprintf("persona %q speaks through qwen3-tts tier %s, which is not installed", p.ID, tier)
		if st.Installed {
			detail = fmt.Sprintf("persona %q pins qwen3-tts tier %s but the installed package lacks it", p.ID, tier)
		}
		nativeURL := ""
		if cfg != nil {
			nativeURL = cfg.QwenTTSNativeURL
		}
		diags = append(diags, config.Diagnostic{
			Name:        "qwen persona " + p.ID,
			Severity:    config.SeverityWarn,
			Detail:      detail,
			Remediation: managedqwen.InstallRemediation(nativeURL),
		})
	}
	return diags
}

// bargeInSpeakersDiags warns when enrolled_only cannot admit voice interruptions
// because live recognition is disabled or no speaker is enrolled. Typed/manual
// interruption remains available. Loading enrollment is read-only.
func bargeInSpeakersDiags(cfg *config.Config) []config.Diagnostic {
	return bargeInSpeakersDiagsNative(cfg)
}

// speakersEnrollmentDiags reports the empty state of the enrollment store, but
// only when live labels are actually on: with speaker.live off nothing shows a
// name anywhere, so there is nothing to fix and doctor stays silent (its norm).
// With live on and no profile, every turn is labelled speaker-1 and the user
// has no way to learn from the running product that registering a voice is
// what changes that.
func speakersEnrollmentDiags(cfg *config.Config) []config.Diagnostic {
	return speakersEnrollmentDiagsNative(cfg)
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Output machine-readable JSON")
	doctorCmd.Flags().BoolVar(&doctorVoiceDevices, "voice-devices", false, "Also probe microphone/speaker availability (touches audio hardware)")
	rootCmd.AddCommand(doctorCmd)
}
