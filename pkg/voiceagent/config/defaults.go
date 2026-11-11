package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
)

// Worker job limits (WI-41c322 D5). The budget is the guarantee: no spawned
// job runs longer than it, and the user sets it.
const (
	DefaultVoiceWorkerMaxConcurrent = 1
	DefaultVoiceWorkerJobBudget     = 0
)

// DefaultKokoroVoice is what an unset tts_voice speaks as.
//
// It is exported because three places outside this function had the literal:
// the schema key's Default, internal/scene (which substitutes it before a
// pooled worker can fall back to whichever cast member built it) and the TUI's
// effective-voice label. Nothing failed when they diverged, and the test that
// looked like it covered it compared the constant against itself.
const DefaultKokoroVoice = "af_heart"

func setDefaults(v *viper.Viper) {
	v.SetDefault("tts_provider", "kokoro")
	v.SetDefault("voice_fallback_provider", "kokoro")
	v.SetDefault("tts_voice", DefaultKokoroVoice)
	v.SetDefault("openai_speech_url", DefaultOpenAISpeechURL)
	v.SetDefault("openai_speech_model", DefaultOpenAISpeechModel)
	v.SetDefault("openai_speech_voice", DefaultOpenAISpeechVoice)
	v.SetDefault("openai_speech_api_key", "")
	v.SetDefault("openai_speech_timeout", DefaultOpenAISpeechTimeout)
	v.SetDefault("openai_speech_runtime", "auto")
	v.SetDefault("openai_speech_backend", "auto")
	v.SetDefault("openai_speech_native_binary", "")
	v.SetDefault("audio8_voice", "")
	v.SetDefault("speech_speed", 0.95)
	v.SetDefault("playback_volume", DefaultPlaybackVolume)
	// Empty selects Obey Voice's managed Qwen runtime. Set an explicit binary and
	// model path only for the advanced external-worker compatibility path.
	v.SetDefault("qwen_tts_binary", "")
	v.SetDefault("qwen_tts_model", "")
	v.SetDefault("qwen_tts_timeout", 120)
	v.SetDefault("qwen_tts_mode", "")
	v.SetDefault("qwen_tts_voice", "")
	v.SetDefault("qwen_tts_language", "")
	v.SetDefault("qwen_tts_instruction", "")
	v.SetDefault("qwen_tts_reference_audio", "")
	v.SetDefault("qwen_tts_reference_text", "")
	v.SetDefault("qwen_tts_consent", false)
	v.SetDefault("qwen_tts_model_tier", "0.6b")
	// Chunk playback follows the measurements, not a preference: auto streams
	// only where synthesis was measured faster than realtime (tts_stream.go).
	v.SetDefault("qwen_tts_stream", QwenStreamAuto)
	v.SetDefault("qwen_tts_workers", DefaultQwenTTSWorkers)
	// Empty → published platform release from qwen3-tts-native (see internal/qwen DefaultNativeRelease).
	v.SetDefault("qwen_tts_native_url", "")
	v.SetDefault("qwen_tts_native_sha256", "")
	v.SetDefault("output_device", "")

	// Speech runs on this computer until an operator says otherwise; LAN is
	// never the compiled default.
	v.SetDefault("tts_place", TTSPlaceLocal)
	v.SetDefault("tts_worker_url", "")
	v.SetDefault("tts_worker_token", "")
	v.SetDefault("tts_lan_strict", false)
	v.SetDefault("tts_lan_timeout_ms", DefaultTTSLanTimeoutMS)
	v.SetDefault("tts_lookahead", DefaultTTSLookahead)

	v.SetDefault("stt_provider", "sherpa-parakeet")
	v.SetDefault("input_device", "")
	v.SetDefault("stt_mode", "")
	v.SetDefault("stt_custom_words", []string{})
	v.SetDefault("stt_custom_words_from_personas", true)
	v.SetDefault("stt_word_correction_threshold", 0.12)
	v.SetDefault("stt_filler_removal", false)
	v.SetDefault("stt_numbers", false)
	v.SetDefault("stt_postprocess_enabled", false)
	v.SetDefault("stt_postprocess_provider", "ollama")
	v.SetDefault("stt_postprocess_model", "")
	v.SetDefault("stt_postprocess_mode", "patch")
	v.SetDefault("stt_postprocess_timeout_ms", 1000)
	v.SetDefault("stt_postprocess_keep_alive", "")
	v.SetDefault("stt_postprocess_candidate_threshold", 0.45)
	v.SetDefault("sherpa_streaming_model", "en-2023-06-26")
	v.SetDefault("whisper_model", "small")
	v.SetDefault("parakeet_model", DefaultParakeetModel)
	v.SetDefault("whisper_quantized", true)
	v.SetDefault("whispercpp_binary", "whisper-cli")
	v.SetDefault("whispercpp_model", "base.en")
	v.SetDefault("whispercpp_model_path", filepath.Join(DefaultModelsDir(), "whispercpp", "ggml-base.en.bin"))
	v.SetDefault("transcribecpp_binary", "transcribe-cli")
	v.SetDefault("transcribecpp_model", DefaultTranscribeCPPModel)

	v.SetDefault("vad_enabled", true)
	v.SetDefault("vad_silence_duration", 1.2)
	// Machine grounding (user, cwd, host, OS) reaches every provider by default.
	v.SetDefault("environment_context_enabled", true)
	v.SetDefault("vad_threshold", 0.6)
	v.SetDefault("vad_min_speech_duration", 0.1)
	v.SetDefault("vad_pre_roll_ms", 300)
	// Off by default: the frontend's noise suppressor over-suppresses normal-volume
	// speech (gates it below the VAD threshold — see voice_frontend_test.go), which
	// left voice mode stuck on "listening" until the user spoke loudly. Re-enabling
	// needs the suppressor re-tuned against real mic input. Its main consumer, AEC
	// for barge-in, is also off by default.
	v.SetDefault("voice_frontend_enabled", false)
	// voice_frontend itself defaults to off (D8: the defaults flip to webrtc
	// is slice 4, gated on the hardware sign-off, not this migration). The
	// legacy bool above still decides an unmigrated config's real behavior —
	// see resolveVoiceFrontendMigration.
	v.SetDefault("voice_frontend", VoiceFrontendOff)
	v.SetDefault("voice_frontend_ns_level", DefaultVoiceFrontendNSLevel)
	v.SetDefault("barge_in_enabled", false)
	v.SetDefault("barge_in_vad_threshold", DefaultBargeInVADThreshold)
	v.SetDefault("barge_in_tail_guard_ms", DefaultBargeInTailGuardMS)
	// barge_in_speakers defaults to anyone (D7): enrolled_only is opt-in
	// policy, not a defaults change (S3 scope).
	v.SetDefault("barge_in_speakers", BargeInSpeakersAnyone)

	// On by default: hold-to-talk is the one input mode that works with the mic
	// muted — nothing is captured until the key is down. The combo defaults to
	// empty rather than a spelling, because the right default differs by OS
	// (⌥Space is free on macOS and is the window menu on Windows) and a value
	// written into config.yaml would follow a home directory across machines.
	v.SetDefault("talk_hotkey_enabled", true)
	v.SetDefault("talk_hotkey_mode", string(talkhotkey.DefaultMode))
	v.SetDefault("talk_hotkey", "")

	// Off by default (D5): the gate has not been evaluated against a room, and
	// on without a voice front-end the agent summons itself out of its own
	// playback. Aliases are empty — the display name and its first word are
	// already the wake words.
	v.SetDefault("name_summon_enabled", false)
	v.SetDefault("acoustic_name_enabled", false)
	v.SetDefault("name_summon_aliases", []string{})

	// Off by default so the terminal keeps its own click-drag selection, which
	// is how every other agent chat behaves — you select transcript text and
	// copy it with the terminal, no modifier and no app-specific mode.
	//
	// Claiming the mouse buys wheel events routed straight to the viewport and
	// costs selection outright, which is the wrong trade for a surface whose
	// whole output is text worth copying. Wheel scrolling survives anyway:
	// terminals in the alternate screen send arrow keys when nothing has
	// claimed the mouse, and an empty composer scrolls the transcript with
	// them. Page Up / Page Down are unconditional.
	v.SetDefault("tui_mouse_enabled", false)

	// Local-first: the default brain is the local Ollama server. A fresh install
	// with no ollama_model fails with an actionable message from NewOllama rather
	// than silently routing to a cloud CLI.
	v.SetDefault("brain_provider", "ollama")
	v.SetDefault("persona_routing_timeout", DefaultPersonaRoutingTimeout)
	v.SetDefault("group_participation_enabled", false)
	v.SetDefault("group_participation_mode", "invited")
	for _, key := range decisionKeys() {
		v.SetDefault(key.Key, key.Default)
	}
	v.SetDefault("voice_work_enabled", false)
	v.SetDefault("voice_workers_disabled", false)
	v.SetDefault("voice_worker_provider", "")
	v.SetDefault("voice_worker_model", "")
	v.SetDefault("voice_worker_ollama_host", "")
	v.SetDefault("voice_worker_max_concurrent", DefaultVoiceWorkerMaxConcurrent)
	v.SetDefault("voice_worker_job_budget", DefaultVoiceWorkerJobBudget)
	v.SetDefault("voice_worker_boundary", true)
	v.SetDefault("grok_model", "")
	v.SetDefault("cursor_model", "")
	v.SetDefault("muse_spark_model", "")
	v.SetDefault("obey_provider", "")
	v.SetDefault("obey_model", "")
	v.SetDefault("obey_camp", "")
	v.SetDefault("obey_socket", "")
	v.SetDefault("obey_tool_policy", "strict")
	v.SetDefault("codex_model", "")
	v.SetDefault("codex_reasoning_effort", "")
	v.SetDefault("ollama_model", "")
	v.SetDefault("ollama_embedding_model", "nomic-embed-text")
	v.SetDefault("skills_similarity_threshold", 0.55)
	v.SetDefault("ollama_host", "http://localhost:11434")
	v.SetDefault("ollama_num_ctx", 32768)
	v.SetDefault("ollama_response_tokens", 4096)
	v.SetDefault("ollama_num_batch", 128)
	v.SetDefault("ollama_keep_alive", "10m")
	// auto: qwen3-family models get Think:true so CoT lands in Message.Thinking
	// instead of spoken Content. Explicit false remains the leaky hatch.
	v.SetDefault("ollama_think", "auto")
	// The CLI session is trusted by default (no silent drops); the fuse is
	// opt-in for unattended/serve setups.
	v.SetDefault("claude_max_session_tokens", 0)
	// ~15.7k of the prompt is the system prompt + CLI preamble before any
	// conversation, and voice turns add only a few hundred tokens each — but a
	// tool-heavy turn can add thousands. 60k leaves a long voice conversation
	// untouched while flagging a runaway session before TTFT suffers.
	v.SetDefault("claude_session_warn_tokens", 60000)
	// Local tools and Agent Skills are on by default so a fresh install can
	// use file tools and SKILL.md discovery without hunting Settings. Explicit
	// false (config key or env) still wins. Remote serve stays default-deny
	// via remote_tools_enabled.
	v.SetDefault("voice_tools_enabled", true)
	v.SetDefault("voice_router_tools", "route_only")
	// Silence until asked: a worker that talks over the conversation is worse
	// than one that is quiet about what it did.
	v.SetDefault("voice_work_narration", "status_only")
	v.SetDefault("voice_failed_route_repair", true)
	v.SetDefault("continuity_integrity_enabled", true)
	v.SetDefault("continuity_projection_enabled", true)
	v.SetDefault("continuity_archive_enabled", true)
	v.SetDefault("continuity_soft_limit_ratio", 0.80)
	v.SetDefault("continuity_compact_filler", false)
	v.SetDefault("continuity_recovery_max", 2)
	// Voice latency and worker task capacity are configured independently.
	v.SetDefault("continuity_num_ctx_ceil", 32768)
	v.SetDefault("voice_worker_num_ctx", 0)
	v.SetDefault("tool_command_timeout", 30)
	v.SetDefault("delegation_enabled", false)
	v.SetDefault("delegation_binary", "obey")
	v.SetDefault("delegation_socket", "")
	v.SetDefault("delegation_campaign_id", "")
	v.SetDefault("delegation_parent_session_id", "")
	v.SetDefault("delegation_profile", "")
	v.SetDefault("delegation_timeout", 300)
	v.SetDefault("remote_tools_enabled", false)

	// Calibre is opt-in. Empty binaries are resolved by bundle-aware LookPath.
	v.SetDefault("calibre_enabled", false)
	v.SetDefault("calibre_library_path", "")
	v.SetDefault("calibredb_binary", "")
	v.SetDefault("calibre_convert_binary", "")
	v.SetDefault("calibre_prefer_format", "epub")

	v.SetDefault("agent_name", "Obey")
	v.SetDefault("user_display_name", "")
	v.SetDefault("active_persona", "obey")
	v.SetDefault("persona", "obey")
	v.SetDefault("prompts_dir", "")
	v.SetDefault("backchannel_enabled", false)
	v.SetDefault("skills_enabled", true)
	v.SetDefault("skills_dir", "")
	v.SetDefault("work_dir", "")
	v.SetDefault("skills_disabled", []string{})
	v.SetDefault("models_dir", DefaultModelsDir())

	v.SetDefault("language", "en-US")
	v.SetDefault("max_history", 40)
	v.SetDefault("listen_timeout", 10)
	v.SetDefault("phrase_time_limit", 30)

	// Meeting notes routing defaults.
	v.SetDefault("meeting.dir", "")
	v.SetDefault("meeting.route.mode", "ask")
	v.SetDefault("meeting.route.default", "")
	// Embed transcript by default so routed intents are self-contained.
	v.SetDefault("meeting.route.body", "full")
	v.SetDefault("meeting.route.destinations", []any{})

	// Speaker analysis on by default: live chat labels + offline meeting
	// diarization. Models install on first use (conversation start / meeting
	// end). Explicit false still wins.
	v.SetDefault("speaker.enabled", true)
	v.SetDefault("speaker.threshold", 0.6)
	v.SetDefault("speaker.enrollment_dir", "")
	v.SetDefault("speaker.live.enabled", true)
	v.SetDefault("speaker.live.mode", "indicator")
	v.SetDefault("speaker.live.threshold", 0.0) // 0 → inherit speaker.threshold
	v.SetDefault("speaker.live.window_ms", 1500)
	v.SetDefault("speaker.meeting.enabled", true)
	v.SetDefault("speaker.meeting.record_audio", false)
	v.SetDefault("speaker.meeting.num_speakers", 0)
	// Live meeting labels default on with master speaker analysis; users can
	// turn them off if CPU cost is unwanted. Offline diarize stays independent.
	v.SetDefault("speaker.meeting.live", true)
	v.SetDefault("speaker.meeting.diarizer", DefaultMeetingDiarizer)
	v.SetDefault("speaker.models.embedding", "")
	v.SetDefault("speaker.models.segmentation", "")
}

// ClampOllamaNumBatch keeps ollama_num_batch from exceeding ollama_num_ctx.
//
// A batch larger than the window is not merely redundant, it is
// self-defeating: llama.cpp places its last reusable context checkpoint about
// one batch before the end of the prompt, so a batch wider than anything that
// can be in the window puts that checkpoint at or before position zero and
// nothing is ever reused — the exact condition ollama_num_batch exists to
// avoid. The schema bounds the key, but a hand-edited config.yaml or
// OLLAMA_NUM_BATCH does not go through the schema, so the guard lives at load
// where every path reaches it.
//
// Zero is left alone: it is the documented escape hatch meaning "let Ollama
// choose". A window of zero means the server sizes it, so there is nothing to
// clamp against and the batch is left as written.
func ClampOllamaNumBatch(numBatch, numCtx int) int {
	if numBatch <= 0 {
		return 0
	}
	if numCtx > 0 && numBatch > numCtx {
		fmt.Fprintf(os.Stderr,
			"obey-voice: ollama_num_batch %d exceeds ollama_num_ctx %d; using %d (a batch wider than the window disables prompt reuse)\n",
			numBatch, numCtx, numCtx)
		return numCtx
	}
	return numBatch
}

// ClampToolCommandTimeout bounds tool_command_timeout to 1–120 seconds.
// Zero/negative becomes the default 30.
func ClampToolCommandTimeout(seconds int) int {
	const (
		def = 30
		min = 1
		max = 120
	)
	if seconds <= 0 {
		return def
	}
	if seconds < min {
		return min
	}
	if seconds > max {
		return max
	}
	return seconds
}
