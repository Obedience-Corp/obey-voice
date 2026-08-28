package config

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/platforminfo"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// Severity classifies a diagnostic result.
type Severity string

const (
	SeverityOK    Severity = "ok"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Diagnostic is one read-only setup check result.
type Diagnostic struct {
	Name        string   `json:"name"`
	Severity    Severity `json:"severity"`
	Detail      string   `json:"detail"`
	Remediation string   `json:"remediation,omitempty"`
}

// Diagnose runs read-only setup checks for cfg against modelsDir and returns
// per-check diagnostics. lookPath resolves external binaries (inject a fake in
// tests; pass exec.LookPath in production). It never downloads or initializes
// providers, so it is safe offline. Missing model assets are warnings (run
// 'models ensure'); unsupported providers and absent required binaries are
// errors. It does not probe the microphone, so batch-only setups are not failed
// for lacking one.
func Diagnose(cfg *Config, modelsDir string, lookPath func(string) (string, error)) []Diagnostic {
	var diags []Diagnostic

	diags = append(diags, diagnoseBrainProvider(cfg, lookPath))

	// Legacy fuse detection: 60000 was the baked default that installs wrote
	// to config.yaml between the session-budget release and the warn-path
	// change, so an explicit 60000 is almost always inherited, not chosen.
	// The shipped default is now 0 (warn instead of silently dropping the
	// CLI session).
	if cfg.ClaudeMaxSessionTokens == 60000 {
		diags = append(diags, Diagnostic{
			Name:        "claude-session-fuse",
			Severity:    SeverityWarn,
			Detail:      "claude_max_session_tokens is 60000 — the old baked default; claude sessions silently drop past it",
			Remediation: "delete the key (or set 0) to adopt warn-only sessions; keep it only if the cap is intentional",
		})
	}

	diags = append(diags, diagnoseWorkDirSource(cfg))

	// A work_dir pointing at a folder that no longer exists is silent otherwise:
	// tools keep working, just somewhere the user did not choose.
	if dir := strings.TrimSpace(cfg.WorkDir); dir != "" {
		if st, err := os.Stat(expandHomePath(dir)); err != nil || !st.IsDir() {
			// The remediation comes from the same helper the provenance row
			// uses, so the two rows cannot advise different fixes: with an
			// exported variable behind the bad path, "point work_dir at a
			// folder that exists" sends the user to a config.yaml that does not
			// hold the key.
			resolved := WorkDirInfoFrom(cfg)
			diags = append(diags, Diagnostic{
				Name:        "work-dir",
				Severity:    SeverityWarn,
				Detail:      fmt.Sprintf("work_dir %q is not an existing directory — the agent falls back to %s", dir, resolved.Dir),
				Remediation: workDirFallbackRemediation(resolved),
			})
		}
	}

	norm, sttErr := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if sttErr == nil {
		diags = append(diags, Diagnostic{Name: "stt-provider", Severity: SeverityOK, Detail: fmt.Sprintf("%s/%s", norm.Provider, norm.Mode)})
		if norm.Provider == STTProviderParakeet {
			if p, err := ParakeetModel(cfg.ParakeetModel); err == nil && p.EnglishOnly() && LanguageCode(cfg.Language) != "en" {
				diags = append(diags, Diagnostic{
					Name:        "stt-language",
					Severity:    SeverityWarn,
					Detail:      fmt.Sprintf("parakeet_model %s transcribes English only; language is %s", p.Name, cfg.Language),
					Remediation: "set parakeet_model tdt-0.6b-v3 (25 languages) or stt_provider sherpa (Whisper)",
				})
			}
		}
	} else {
		diags = append(diags, Diagnostic{
			Name:        "stt-provider",
			Severity:    SeverityError,
			Detail:      sttErr.Error(),
			Remediation: "set a supported stt_provider/stt_mode combination",
		})
	}

	if strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), qwen.ProviderName) && TTSPlaceOf(cfg) == TTSPlaceLAN {
		// The speech worker owns the binary, the GGUF and the tier; this host
		// needs none of them. Reachability is the speech-place row.
		diags = append(diags, Diagnostic{
			Name:     "tts-provider",
			Severity: SeverityOK,
			Detail:   "qwen3-tts (speech worker on another computer)",
		})
		if err := ValidateQwenTTSConfig(cfg); err != nil {
			diags = append(diags, Diagnostic{
				Name:        "qwen3-tts-voice-controls",
				Severity:    SeverityError,
				Detail:      err.Error(),
				Remediation: "select a speaker and mode the speech worker advertises",
			})
		}
	} else if strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), qwen.ProviderName) {
		binary := strings.TrimSpace(cfg.QwenTTSBinary)
		model := strings.TrimSpace(cfg.QwenTTSModel)
		managed := qwen.UseManaged(binary, model)
		native := qwen.InspectNative(modelsDir, cfg.QwenTTSModelTier)
		providerDetail := "qwen3-tts (external default-voice worker)"
		controlsDetail := "external model-native default/static synthesis"
		controlsRemediation := "clear unsupported voice controls for the external worker"
		if managed && native.Installed {
			providerDetail = "qwen3-tts (native warm worker + CustomVoice presets)"
			controlsDetail = "native CustomVoice speaker selection"
			controlsRemediation = "select a speaker and mode advertised in Settings → Voice"
		} else if managed {
			providerDetail = "qwen3-tts (product path; native package required)"
			controlsDetail = "native CustomVoice speaker selection"
			controlsRemediation = "run 'obey-voice models ensure --tts' to install the native package"
		} else if isNativeWorkerBinary(binary) {
			providerDetail = "qwen3-tts (explicit native worker)"
			controlsDetail = "native CustomVoice speaker selection"
			controlsRemediation = "select a speaker and mode advertised in Settings → Voice"
		}
		diags = append(diags, Diagnostic{
			Name:     "tts-provider",
			Severity: SeverityOK,
			Detail:   providerDetail,
		})
		if err := ValidateQwenTTSConfig(cfg); err != nil {
			diags = append(diags, Diagnostic{
				Name:        "qwen3-tts-voice-controls",
				Severity:    SeverityError,
				Detail:      err.Error(),
				Remediation: controlsRemediation,
			})
		} else {
			diags = append(diags, Diagnostic{
				Name:     "qwen3-tts-voice-controls",
				Severity: SeverityOK,
				Detail:   controlsDetail,
			})
		}

		if managed {
			if native.Installed {
				diags = append(diags, Diagnostic{Name: "qwen3-tts-binary", Severity: SeverityOK, Detail: native.Worker})
				diags = append(diags, Diagnostic{Name: "qwen3-tts-model", Severity: SeverityOK, Detail: native.ModelDir + " (tier " + native.DefaultTier + ")"})
				diags = append(diags, Diagnostic{
					Name: "qwen3-tts-runtime-libs", Severity: SeverityOK,
					Detail: "libqwen3tts + libggml present next to worker",
				})
				if len(native.TiersReady) > 0 {
					diags = append(diags, Diagnostic{
						Name: "qwen3-tts-tiers", Severity: SeverityOK,
						Detail: "ready: " + strings.Join(native.TiersReady, ", "),
					})
				}
				if len(native.TiersMissing) > 0 {
					diags = append(diags, Diagnostic{
						Name: "qwen3-tts-tiers-optional", Severity: SeverityWarn,
						Detail:      "not in package: " + strings.Join(native.TiersMissing, ", "),
						Remediation: "install a multi-tier native release or keep qwen_tts_model_tier=0.6b",
					})
				}
				if d := qwenTierRAMAdvice(native.DefaultTier, systemMemoryBytes()); d != nil {
					diags = append(diags, *d)
				}
			} else if native.WorkerReady && native.ModelReady && !native.RuntimeReady {
				// Incomplete portable package: worker+GGUF exist but shared libs
				// (especially libggml*) are missing — this was the Darwin mid-reply
				// dyld / Kokoro-fallback failure mode.
				diags = append(diags, Diagnostic{
					Name:        "qwen3-tts-runtime-libs",
					Severity:    SeverityError,
					Detail:      native.Detail,
					Remediation: "run 'obey-voice models ensure --tts' with a portable qwen3-tts-native release that ships libggml* next to the worker (or set qwen_tts_native_url to that tarball)",
				})
				diags = append(diags, Diagnostic{Name: "qwen3-tts-binary", Severity: SeverityWarn, Detail: native.Worker + " (worker present; runtime libs incomplete)"})
				diags = append(diags, Diagnostic{Name: "qwen3-tts-model", Severity: SeverityOK, Detail: native.ModelDir + " (tier " + native.DefaultTier + ")"})
			} else {
				diags = append(diags, Diagnostic{
					Name:        "qwen3-tts-binary",
					Severity:    SeverityError,
					Detail:      "native Qwen3-TTS package is not installed under models/qwen3-tts",
					Remediation: "run 'obey-voice models ensure --tts' (downloads the published platform package; override with qwen_tts_native_url if needed)",
				})
				diags = append(diags, Diagnostic{
					Name:        "qwen3-tts-model",
					Severity:    SeverityError,
					Detail:      "native Qwen GGUF models are not installed",
					Remediation: "run 'obey-voice models ensure --tts'",
				})
			}
		} else {
			if binary == "" {
				binary = "qwen3-tts-cli"
			}
			if _, err := lookPath(binary); err != nil {
				diags = append(diags, Diagnostic{
					Name:        "qwen3-tts-binary",
					Severity:    SeverityError,
					Detail:      fmt.Sprintf("native Qwen3-TTS worker %q not found in PATH", binary),
					Remediation: "install qwen3-tts.cpp's qwen3-tts-cli and set qwen_tts_binary if needed",
				})
			} else {
				diags = append(diags, Diagnostic{Name: "qwen3-tts-binary", Severity: SeverityOK, Detail: binary})
			}
			if model == "" {
				diags = append(diags, Diagnostic{
					Name: "qwen3-tts-model", Severity: SeverityError, Detail: "qwen_tts_model is not configured",
					Remediation: "set qwen_tts_model, or clear qwen_tts_binary/model to use the native package under models/qwen3-tts",
				})
			} else if info, err := os.Stat(model); err != nil || !info.IsDir() {
				diags = append(diags, Diagnostic{
					Name: "qwen3-tts-model", Severity: SeverityError,
					Detail:      fmt.Sprintf("Qwen3-TTS model directory %q is unavailable", model),
					Remediation: "install/convert the Qwen3-TTS native model artifacts and set qwen_tts_model",
				})
			} else {
				diags = append(diags, Diagnostic{Name: "qwen3-tts-model", Severity: SeverityOK, Detail: model})
			}
		}
	} else if ManagedTTS(cfg) {
		diags = append(diags, Diagnostic{
			Name:     "tts-provider",
			Severity: SeverityOK,
			Detail:   "kokoro (" + KokoroPack() + ")",
		})
	} else if strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), "audio8") {
		// audio8 is a real provider that obey-voice does not manage assets
		// for, so it needs its own row: the unmanaged fallthrough below would
		// tell the user to abandon a provider that works once its export is on
		// disk. `models ensure` installs nothing here (ManagedTTS excludes it),
		// so the remediation must not point at it.
		dir := filepath.Join(modelsDir, Audio8Subdir)
		if Audio8ExportInstalled(dir) {
			diags = append(diags, Diagnostic{
				Name:     "tts-provider",
				Severity: SeverityOK,
				Detail:   "audio8 (experimental; export at " + dir + ")",
			})
		} else {
			diags = append(diags, Diagnostic{
				Name:        "tts-provider",
				Severity:    SeverityWarn,
				Detail:      "audio8 is selected but no export is installed at " + dir,
				Remediation: "install the Audio8 ONNX export into " + dir + " (obey-voice does not download it; see scripts/audio8_bench/README.md for the pinned snapshot)",
			})
		}
	} else {
		diags = append(diags, Diagnostic{
			Name:        "tts-provider",
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("tts_provider %q is not a known provider", cfg.TTSProvider),
			Remediation: "set tts_provider=kokoro, tts_provider=qwen3-tts (with its native worker and model configured), or tts_provider=audio8 (with the Audio8 export installed)",
		})
	}

	// Barge-in monitors the mic while Samantha speaks, so without the voice
	// frontend's echo cancellation her own playback can trip it.
	if cfg.BargeInEnabled && !cfg.VoiceFrontendEnabled {
		diags = append(diags, Diagnostic{
			Name:        "barge-in-echo",
			Severity:    SeverityWarn,
			Detail:      "barge_in_enabled without voice_frontend_enabled: playback echo may trigger barge-in",
			Remediation: "set voice_frontend_enabled=true when using barge-in",
		})
	}

	// File tools only run when the local tools gate is on (pipeline passes
	// VoiceToolsEnabled into every brain turn). Ollama users hit this most.
	if strings.EqualFold(strings.TrimSpace(cfg.BrainProvider), "ollama") {
		if cfg.VoiceToolsEnabled {
			diags = append(diags, Diagnostic{
				Name:     "voice-tools",
				Severity: SeverityOK,
				Detail:   "voice_tools_enabled=true (list_files/read_file/write_file/run_command/web_search/fetch_url available)",
			})
		} else {
			diags = append(diags, Diagnostic{
				Name:        "voice-tools",
				Severity:    SeverityWarn,
				Detail:      "voice_tools_enabled=false — Ollama will not read or write files",
				Remediation: "set voice_tools_enabled=true in config, or VOICE_TOOLS_ENABLED=true",
			})
		}
		if cfg.SkillsEnabled {
			dir := cfg.SkillsDir
			if dir == "" {
				dir = SkillsDir()
			}
			diags = append(diags, Diagnostic{
				Name:     "skills",
				Severity: SeverityOK,
				Detail:   fmt.Sprintf("skills_enabled=true (semantic model %q; scan cwd/.agents/skills, nearest ancestor .agents/skills, ~/.agents/skills, then %s)", cfg.OllamaEmbeddingModel, dir),
			})
		} else {
			diags = append(diags, Diagnostic{
				Name:     "skills",
				Severity: SeverityOK,
				Detail:   "skills_enabled=false (Agent Skills not advertised to Ollama)",
			})
		}
	}

	// whisper.cpp shells out to an external CLI; check it only when selected.
	if sttErr == nil && norm.Provider == STTProviderWhisperCPP {
		bin := strings.TrimSpace(cfg.WhisperCPPBinary)
		if _, err := lookPath(bin); err != nil {
			diags = append(diags, Diagnostic{
				Name:        "whispercpp-binary",
				Severity:    SeverityError,
				Detail:      fmt.Sprintf("whisper.cpp binary %q not found in PATH", bin),
				Remediation: "install whisper.cpp and put its CLI on PATH, or set whispercpp_binary",
			})
		} else {
			diags = append(diags, Diagnostic{Name: "whispercpp-binary", Severity: SeverityOK, Detail: bin})
		}
	}

	// transcribe.cpp is the same shape: an external CLI plus a managed GGUF,
	// checked only when it is the selected provider.
	if sttErr == nil && norm.Provider == STTProviderTranscribeCPP {
		diags = append(diags, diagnoseTranscribeCPP(cfg, modelsDir, lookPath))
	}

	// The optional Sortformer meeting-review diarizer shares that CLI.
	_, diarErr := MeetingDiarizerFrom(cfg)
	diags = append(diags, diagnoseMeetingDiarizer(cfg, modelsDir, lookPath)...)

	// Skip STT and speaker asset resolution when their config already failed
	// above, so the model-assets check stays about model names rather than
	// repeating what a dedicated row just said.
	req := DefaultAssetRequest(cfg)
	req.NeedSTT = req.NeedSTT && sttErr == nil
	req.NeedSpeaker = req.NeedSpeaker && diarErr == nil
	if m, err := ManifestFor(cfg, req); err != nil {
		diags = append(diags, Diagnostic{
			Name:        "model-assets",
			Severity:    SeverityError,
			Detail:      err.Error(),
			Remediation: "fix the configured model name",
		})
	} else {
		for _, s := range m.Status(modelsDir) {
			if s.Installed {
				diags = append(diags, Diagnostic{Name: "asset:" + s.ID, Severity: SeverityOK, Detail: s.Name + " installed"})
			} else {
				diags = append(diags, Diagnostic{
					Name:        "asset:" + s.ID,
					Severity:    SeverityWarn,
					Detail:      s.Name + " missing",
					Remediation: "run 'obey-voice models ensure'",
				})
			}
		}
	}

	// pdftotext (Poppler) is optional; missing is a warning so non-PDF users stay clean.
	if _, err := lookPath("pdftotext"); err != nil {
		diags = append(diags, Diagnostic{
			Name:        "pdftotext-binary",
			Severity:    SeverityWarn,
			Detail:      "pdftotext not found in PATH (optional; needed for PDF narrate/render)",
			Remediation: platforminfo.PopplerInstallRemediation(runtime.GOOS),
		})
	} else {
		diags = append(diags, Diagnostic{Name: "pdftotext-binary", Severity: SeverityOK, Detail: "pdftotext"})
	}

	// Calibre is optional; always report availability as OK/Warn, never Error.
	// Production callers should pass a bundle-aware lookPath so the macOS app
	// bundle is found without PATH; tests inject fakes.
	calibreBin := strings.TrimSpace(cfg.CalibredbBinary)
	if calibreBin == "" {
		calibreBin = "calibredb"
	}
	if p, err := lookPath(calibreBin); err != nil {
		diags = append(diags, Diagnostic{
			Name:     "calibre-binary",
			Severity: SeverityWarn,
			Detail:   "calibredb not found (optional ebook catalog; Library TUI / library CLI / --from-library)",
			Remediation: platforminfo.CalibreInstallRemediation(runtime.GOOS) + ". " +
				platforminfo.CalibreBinaryHint(runtime.GOOS) + ".",
		})
	} else {
		var detail string
		if cfg.CalibreEnabled {
			detail = p + " (calibre_enabled=true)"
		} else {
			detail = p + " (found; enable with: obey-voice config calibre_enabled true — or press e in Library)"
		}
		diags = append(diags, Diagnostic{Name: "calibre-binary", Severity: SeverityOK, Detail: detail})
	}

	return diags
}

// diagnoseWorkDirSource reports which layer decided the working directory.
//
// A launch decided by the environment or by a fallback is the silent case: the
// config file shows nothing, the banner reads the same as always, and the agent
// works in a directory — and a campaign — nobody chose. Those two warn. A flag,
// the setting, and the launch directory are all things the user did, so they
// report OK rather than staying silent: seeing the source named is how the
// warning cases become recognizable.
func diagnoseWorkDirSource(cfg *Config) Diagnostic {
	info := WorkDirInfoFrom(cfg)
	d := Diagnostic{
		Name:     "work-dir-source",
		Severity: SeverityOK,
		Detail:   fmt.Sprintf("%s (from %s)", info.Dir, info.Describe()),
	}
	switch info.Source {
	case WorkDirSourceEnv:
		d.Severity = SeverityWarn
		d.Detail = fmt.Sprintf("%s comes from the %s environment variable, which outranks work_dir in config.yaml and the launch directory", info.Dir, info.EnvVar)
		d.Remediation = workDirEnvRemediation(info.EnvVar)
	case WorkDirSourceHomeFallback, WorkDirSourceTempFallback:
		d.Severity = SeverityWarn
		d.Detail = fmt.Sprintf("%s is %s", info.Dir, info.Describe())
		d.Remediation = workDirFallbackRemediation(info)
	}
	if c := CampaignFor(info.Dir); c.Found() {
		d.Detail += fmt.Sprintf("; campaign %s", c.Describe())
	}
	return d
}

// workDirEnvRemediation tells the user how to take the directory back, in terms
// of the variable that actually decided it. The hijack framing belongs to the
// bare name only: OBEY_VOICE_WORK_DIR is the documented override, and calling a
// deliberate export a shell accident teaches the user to distrust the right
// answer. Neither wording tells them to "set work_dir" — the environment
// outranks that key, so they may have set it already.
func workDirEnvRemediation(envVar string) string {
	if envVar == legacyWorkDirEnvName {
		return fmt.Sprintf("unset %s — a bare %s exported by a shell or .envrc for its own purposes hijacks every launch; use %s when the override is deliberate",
			legacyWorkDirEnvName, legacyWorkDirEnvName, workDirEnvName)
	}
	return fmt.Sprintf("unset %s to let work_dir or the launch directory decide again", envVar)
}

// workDirFallbackRemediation points at whatever named the unusable path, since
// that is the thing to edit — "set work_dir" is wrong advice when an exported
// variable is what pointed at a directory that does not exist.
func workDirFallbackRemediation(info WorkDirInfo) string {
	if info.RejectedSource == WorkDirSourceEnv && info.EnvVar != "" {
		return fmt.Sprintf("point %s at a folder that exists, or unset it to use work_dir or the launch directory", info.EnvVar)
	}
	return "set work_dir to a folder that exists, or clear it to use the directory Obey Voice was launched from"
}

func diagnoseBrainProvider(cfg *Config, lookPath func(string) (string, error)) Diagnostic {
	provider := strings.ToLower(strings.TrimSpace(cfg.BrainProvider))
	if provider == "" {
		provider = "ollama"
	}

	switch provider {
	case "claude", "grok":
		path, err := lookPath(provider)
		if err != nil {
			return Diagnostic{
				Name:        "brain-provider",
				Severity:    SeverityError,
				Detail:      fmt.Sprintf("%s CLI not found on PATH", provider),
				Remediation: fmt.Sprintf("install the %s CLI and ensure %q is on PATH, or select another brain_provider", provider, provider),
			}
		}
		return Diagnostic{
			Name:     "brain-provider",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("%s CLI (%s)", provider, path),
		}
	case "ollama":
		model := strings.TrimSpace(cfg.OllamaModel)
		if model == "" {
			return Diagnostic{
				Name:        "brain-provider",
				Severity:    SeverityError,
				Detail:      "ollama_model is not configured",
				Remediation: "run: obey-voice config ollama_model <model>",
			}
		}
		host := strings.TrimSpace(cfg.OllamaHost)
		parsed, err := url.Parse(host)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Diagnostic{
				Name:        "brain-provider",
				Severity:    SeverityError,
				Detail:      fmt.Sprintf("invalid ollama_host %q", host),
				Remediation: "set ollama_host to an http:// or https:// URL",
			}
		}
		return Diagnostic{
			Name:     "brain-provider",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("ollama model %q at %s (connectivity is not probed by offline doctor)", model, host),
		}
	default:
		return Diagnostic{
			Name:        "brain-provider",
			Severity:    SeverityError,
			Detail:      fmt.Sprintf("unsupported brain_provider %q", cfg.BrainProvider),
			Remediation: "set brain_provider to claude, grok, or ollama",
		}
	}
}

// VoiceDeviceChecker probes audio hardware availability. Implementations must
// honor ctx cancellation and deadlines so a wedged backend cannot hang doctor.
type VoiceDeviceChecker interface {
	CaptureDevices(ctx context.Context) ([]string, error)
	PlaybackDevices(ctx context.Context) ([]string, error)
}

// DiagnoseSpeechPlace reports where speech runs and, for tts_place=lan,
// whether that worker answers. Local never dials. probe is injectable for
// tests; nil uses ProbeSpeechWorker. The row is a warning by default because a
// missing speech worker must not stop a conversation (D7) — only tts_lan_strict
// makes it an error, and then talk does not start either.
func DiagnoseSpeechPlace(ctx context.Context, cfg *Config, probe SpeechProbe) []Diagnostic {
	place := TTSPlaceOf(cfg)
	if place != TTSPlaceLAN {
		return []Diagnostic{{
			Name:     "speech-place",
			Severity: SeverityOK,
			Detail:   "local (speech runs on this computer)",
		}}
	}
	severity := SeverityWarn
	if cfg != nil && cfg.TTSLanStrict {
		severity = SeverityError
	}
	if err := ValidateTTSPlace(cfg); err != nil {
		return []Diagnostic{{
			Name:        "speech-place",
			Severity:    SeverityError,
			Detail:      err.Error(),
			Remediation: speechPlaceRemediation,
		}}
	}
	if probe == nil {
		probe = ProbeSpeechWorker
	}
	result, err := probe(ctx, cfg)
	if err != nil {
		return []Diagnostic{{
			Name:        "speech-place",
			Severity:    severity,
			Detail:      fmt.Sprintf("speech worker did not answer: %v", err),
			Remediation: speechPlaceRemediation,
		}}
	}
	detail := fmt.Sprintf("lan %s (rtt %s, streaming=%t)", result.Host, formatProbeRTT(result.RTT), result.Streaming)
	if tier := strings.TrimSpace(result.Tier); tier != "" {
		detail += ", tier " + tier
	}
	return []Diagnostic{{Name: "speech-place", Severity: SeverityOK, Detail: detail}}
}

// DiagnoseQwenStreaming reports whether this host will play Qwen's PCM chunks
// as they arrive, and why.
//
// It is a statement about policy, not about a live worker: answering for
// certain would mean starting one, and doctor never does. The row therefore
// says what this configuration decides *for a worker that can stream*, which
// is the half an operator can act on — the other half is the worker's own
// ready line, which the speech-place row already prints for lan.
//
// It exists because the decision is otherwise invisible. Streaming is the
// difference between speaking on the first chunk and speaking after the whole
// sentence, and on the wrong backend it is the difference between a fluent
// reply and one that breaks up mid-word.
func DiagnoseQwenStreaming(cfg *Config, modelsDir string) []Diagnostic {
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), qwen.ProviderName) {
		return nil
	}
	if err := ValidateQwenTTSStream(cfg); err != nil {
		return []Diagnostic{{
			Name:        "qwen3-tts-streaming",
			Severity:    SeverityError,
			Detail:      err.Error(),
			Remediation: "set qwen_tts_stream to auto, on or off",
		}}
	}

	mode := QwenTTSStreamOf(cfg)
	remote := TTSPlaceOf(cfg) == TTSPlaceLAN
	backend := ""
	installed := false
	if !remote {
		native := qwen.InspectNative(modelsDir, cfg.QwenTTSModelTier)
		backend = native.BackendHint
		installed = native.Installed
	}
	eligible := QwenStreamEligible(mode, remote, backend, true)

	detail := fmt.Sprintf("%s: chunks play as they arrive on a worker that streams", mode)
	remediation := ""
	if !eligible {
		detail = fmt.Sprintf("%s: whole sentences (chunk playback off)", mode)
	}
	switch {
	case mode != QwenStreamAuto:
		detail += ", set explicitly"
	case remote:
		detail += "; lan is the offload target streaming was measured on"
	case eligible:
		detail += "; this package reports backend " + backend
	case backend == "" && installed:
		// An install predating backend_hint. Pointing at a missing package
		// here would send the operator to reinstall something already present.
		detail += "; the installed package predates the backend field, and only CUDA has been measured safe"
		remediation = "reinstall with 'obey-voice models ensure --tts' so the package reports its backend, or set qwen_tts_stream=on after checking the reply does not break up"
	case backend == "":
		detail += "; no installed package to report a backend, and only CUDA has been measured safe"
		remediation = "set qwen_tts_stream=on to stream anyway, after checking the reply does not break up"
	default:
		detail += "; backend " + backend + " synthesizes slower than it plays, so chunks would run dry mid-word"
		remediation = "leave it off, or set tts_place=lan to synthesize on a CUDA computer"
	}
	return []Diagnostic{{
		Name:        "qwen3-tts-streaming",
		Severity:    SeverityOK,
		Detail:      detail,
		Remediation: remediation,
	}}
}

// formatProbeRTT keeps a loopback probe from reporting "0s": on the same
// machine the round trip is microseconds, and rounding it away hides whether
// the probe ran at all.
func formatProbeRTT(d time.Duration) string {
	if d < 10*time.Millisecond {
		return d.Round(time.Microsecond).String()
	}
	return d.Round(time.Millisecond).String()
}

const speechPlaceRemediation = "start speech-serve on the speech computer or set tts_place=local"

// DiagnoseVoiceDevices checks microphone and speaker availability through
// checker. It is opt-in (doctor --voice-devices) because it touches audio
// hardware; the default Diagnose stays read-only and hardware-free. ctx should
// carry a short timeout.
func DiagnoseVoiceDevices(ctx context.Context, checker VoiceDeviceChecker) []Diagnostic {
	return []Diagnostic{
		voiceDeviceDiagnostic(ctx, "voice:microphone",
			"connect a microphone and allow microphone access for your terminal in OS privacy settings",
			checker.CaptureDevices),
		voiceDeviceDiagnostic(ctx, "voice:speaker",
			"connect or enable an output device in OS sound settings",
			checker.PlaybackDevices),
	}
}

func voiceDeviceDiagnostic(ctx context.Context, name, remediation string, list func(context.Context) ([]string, error)) Diagnostic {
	devices, err := list(ctx)
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return Diagnostic{
			Name:        name,
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("device probe timed out: %v", err),
			Remediation: "audio backend did not respond; close other apps using audio and retry, or check OS audio permissions",
		}
	case err != nil:
		return Diagnostic{
			Name:        name,
			Severity:    SeverityError,
			Detail:      fmt.Sprintf("device probe failed: %v", err),
			Remediation: remediation,
		}
	case len(devices) == 0:
		return Diagnostic{
			Name:        name,
			Severity:    SeverityError,
			Detail:      "no devices found",
			Remediation: remediation,
		}
	default:
		return Diagnostic{
			Name:     name,
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("%d device(s): %s", len(devices), strings.Join(devices, ", ")),
		}
	}
}

// HasErrors reports whether any diagnostic has error severity.
func HasErrors(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}
