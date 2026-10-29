package config

import (
	"os"
	"strings"
)

// envBindings is the only place a config key is bound to an environment
// variable. Explicit bindings only — no AutomaticEnv, which would bind every
// key to its bare upper-cased name and let unrelated vars like the standard
// LANGUAGE leak into (and get persisted over) config values. The settings
// schema reports a key's binding from this same map, so `config schema` can
// never claim a binding the loader does not honor.
var envBindings = map[string]string{
	"tts_provider":                "TTS_PROVIDER",
	"voice_fallback_provider":     "VOICE_FALLBACK_PROVIDER",
	"tts_voice":                   "TTS_VOICE",
	"audio8_voice":                "AUDIO8_VOICE",
	"openai_speech_url":           "OPENAI_SPEECH_URL",
	"openai_speech_model":         "OPENAI_SPEECH_MODEL",
	"openai_speech_voice":         "OPENAI_SPEECH_VOICE",
	"openai_speech_api_key":       "OPENAI_SPEECH_API_KEY",
	"openai_speech_timeout":       "OPENAI_SPEECH_TIMEOUT",
	"tts_worker_token":            "OBEY_SPEECH_TOKEN",
	"tts_lookahead":               "TTS_LOOKAHEAD",
	"qwen_tts_binary":             "QWEN_TTS_BINARY",
	"qwen_tts_model":              "QWEN_TTS_MODEL",
	"qwen_tts_timeout":            "QWEN_TTS_TIMEOUT",
	"qwen_tts_mode":               "QWEN_TTS_MODE",
	"qwen_tts_voice":              "QWEN_TTS_VOICE",
	"qwen_tts_language":           "QWEN_TTS_LANGUAGE",
	"qwen_tts_instruction":        "QWEN_TTS_INSTRUCTION",
	"qwen_tts_reference_audio":    "QWEN_TTS_REFERENCE_AUDIO",
	"qwen_tts_reference_text":     "QWEN_TTS_REFERENCE_TEXT",
	"qwen_tts_consent":            "QWEN_TTS_CONSENT",
	"qwen_tts_model_tier":         "QWEN_TTS_MODEL_TIER",
	"qwen_tts_stream":             "QWEN_TTS_STREAM",
	"qwen_tts_workers":            "QWEN_TTS_WORKERS",
	"qwen_tts_native_url":         "QWEN_TTS_NATIVE_URL",
	"qwen_tts_native_sha256":      "QWEN_TTS_NATIVE_SHA256",
	"output_device":               "OUTPUT_DEVICE",
	"stt_provider":                "STT_PROVIDER",
	"input_device":                "INPUT_DEVICE",
	"stt_mode":                    "STT_MODE",
	"sherpa_streaming_model":      "SHERPA_STREAMING_MODEL",
	"whisper_model":               "WHISPER_MODEL",
	"parakeet_model":              "PARAKEET_MODEL",
	"whispercpp_binary":           "WHISPERCPP_BINARY",
	"whispercpp_model":            "WHISPERCPP_MODEL",
	"whispercpp_model_path":       "WHISPERCPP_MODEL_PATH",
	"transcribecpp_binary":        "TRANSCRIBECPP_BINARY",
	"transcribecpp_model":         "TRANSCRIBECPP_MODEL",
	"models_dir":                  "MODELS_DIR",
	"brain_provider":              "BRAIN_PROVIDER",
	"group_participation_enabled": "GROUP_PARTICIPATION_ENABLED",
	"group_participation_mode":    "GROUP_PARTICIPATION_MODE",
	"persona_routing_timeout":     "PERSONA_ROUTING_TIMEOUT",
	"grok_model":                  "GROK_MODEL",
	"cursor_model":                "CURSOR_MODEL",
	"muse_spark_model":            "MUSE_SPARK_MODEL",
	"codex_model":                 "CODEX_MODEL",
	"codex_reasoning_effort":      "CODEX_REASONING_EFFORT",
	"ollama_model":                "OLLAMA_MODEL",
	"ollama_embedding_model":      "OLLAMA_EMBEDDING_MODEL",
	"ollama_host":                 "OLLAMA_HOST",
	"ollama_num_ctx":              "OLLAMA_NUM_CTX",
	"voice_worker_num_ctx":        "VOICE_WORKER_NUM_CTX",
	"ollama_num_batch":            "OLLAMA_NUM_BATCH",
	"ollama_keep_alive":           "OLLAMA_KEEP_ALIVE",
	"ollama_think":                "OLLAMA_THINK",
	"claude_max_session_tokens":   "CLAUDE_MAX_SESSION_TOKENS",
	"claude_session_warn_tokens":  "CLAUDE_SESSION_WARN_TOKENS",
	"voice_tools_enabled":         "VOICE_TOOLS_ENABLED",
	"voice_router_tools":          "VOICE_ROUTER_TOOLS",
	"voice_work_enabled":          "VOICE_WORK_ENABLED",
	"voice_work_narration":        "VOICE_WORK_NARRATION",
	"voice_failed_route_repair":   "VOICE_FAILED_ROUTE_REPAIR",
	"tool_command_timeout":        "TOOL_COMMAND_TIMEOUT",
	"persona":                     "PERSONA",
	"compact_prompt":              "COMPACT_PROMPT",
	"active_persona":              "ACTIVE_PERSONA",
	"prompts_dir":                 "PROMPTS_DIR",
	"backchannel_enabled":         "BACKCHANNEL_ENABLED",
	"skills_enabled":              "SKILLS_ENABLED",
	"skills_dir":                  "SKILLS_DIR",
	"work_dir":                    workDirEnvName,
	"barge_in_enabled":            "BARGE_IN_ENABLED",
	"barge_in_vad_threshold":      "BARGE_IN_VAD_THRESHOLD",
	"barge_in_tail_guard_ms":      "BARGE_IN_TAIL_GUARD_MS",
	"barge_in_speakers":           "BARGE_IN_SPEAKERS",
	"talk_hotkey_enabled":         "TALK_HOTKEY_ENABLED",
	"talk_hotkey_mode":            "TALK_HOTKEY_MODE",
	"talk_hotkey":                 "TALK_HOTKEY",
	"acoustic_name_enabled":       "ACOUSTIC_NAME_ENABLED",
	"name_summon_enabled":         "NAME_SUMMON_ENABLED",
	"vad_threshold":               "VAD_THRESHOLD",
	"vad_min_speech_duration":     "VAD_MIN_SPEECH_DURATION",
	"vad_silence_duration":        "VAD_SILENCE_DURATION",
	"environment_context_enabled": "ENVIRONMENT_CONTEXT_ENABLED",
	"voice_frontend_enabled":      "VOICE_FRONTEND_ENABLED",
	"voice_frontend":              "VOICE_FRONTEND",
	"voice_frontend_ns_level":     "VOICE_FRONTEND_NS_LEVEL",
	"tui_mouse_enabled":           "TUI_MOUSE_ENABLED",
	"calibre_enabled":             "CALIBRE_ENABLED",
	"calibre_library_path":        "CALIBRE_LIBRARY_PATH",
	"calibredb_binary":            "CALIBREDB_BINARY",
	"calibre_convert_binary":      "CALIBRE_CONVERT_BINARY",
	"calibre_prefer_format":       "CALIBRE_PREFER_FORMAT",

	"skills_similarity_threshold": "SKILLS_SIMILARITY_THRESHOLD",
}

// envAliases lists extra environment variables a key still honors, after the
// documented binding in envBindings. Aliases are compatibility only: the schema
// and `config get` report the documented name, and a launch decided by an alias
// is called out by name wherever provenance is shown.
//
// work_dir is the only entry, and the reason the mechanism exists. It was bound
// to the bare, generic WORK_DIR — a name any shell or .envrc can export for its
// own purposes — so an unrelated export silently moved the agent's tools, its
// skills roots and its campaign, with nothing in config.yaml to explain it
// (WI-742a80). The prefixed name is now the documented override and wins when
// both are set; the bare name keeps working so existing setups do not break.
var envAliases = map[string][]string{
	"work_dir": {legacyWorkDirEnvName},
}

const (
	// workDirEnvName is the documented environment override for work_dir;
	// legacyWorkDirEnvName is the bare name it replaced. Both are named because
	// the provenance surfaces have to tell one from the other: unsetting the
	// documented override is a choice, while a bare WORK_DIR is usually an
	// accident belonging to some other tool.
	workDirEnvName       = "OBEY_VOICE_WORK_DIR"
	legacyWorkDirEnvName = "WORK_DIR"
)

// envNamesFor lists every environment variable bound to key, in resolution
// order: the documented binding first, then compatibility aliases. It is the
// order loadLocked registers with viper, which honors the first name that is
// set, so anything reasoning about environment precedence reads it from here.
func envNamesFor(key string) []string {
	primary := envBindings[key]
	if primary == "" {
		return nil
	}
	aliases := envAliases[key]
	names := make([]string, 0, 1+len(aliases))
	return append(append(names, primary), aliases...)
}

// envValueFor reports the environment variable currently deciding key, by
// viper's own rule: the first bound name that is set to a non-empty value.
// Empty is not "set" here because AllowEmptyEnv is off, so an empty variable
// does not reach the merged value and must not be blamed for it.
func envValueFor(key string) (name, value string, ok bool) {
	for _, candidate := range envNamesFor(key) {
		if raw, found := os.LookupEnv(candidate); found && raw != "" {
			return candidate, raw, true
		}
	}
	return "", "", false
}

// envForcesFalse reports whether env is an explicit false-ish value.
func envForcesFalse(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}
