package config

// Config holds all application configuration.
type Config struct {
	// PersonaWorkerTiers is a runtime binding, persisted only in persona.yaml.
	PersonaWorkerTiers *WorkerTierOverrides `mapstructure:"-" yaml:"-" json:"-"`
	// VoiceWorkers is nil for an installation that has not explicitly migrated
	// from the singleton worker. A non-nil value is the complete versioned
	// tier configuration and is authoritative over the legacy fields below.
	VoiceWorkers *VoiceWorkers `mapstructure:"voice_workers" yaml:"voice_workers,omitempty" json:"voice_workers,omitempty"`
	// Delegation is opt-in and bound to an operator-selected Obey parent.
	DelegationEnabled         bool   `mapstructure:"delegation_enabled"`
	DelegationBinary          string `mapstructure:"delegation_binary"`
	DelegationSocket          string `mapstructure:"delegation_socket"`
	DelegationCampaignID      string `mapstructure:"delegation_campaign_id"`
	DelegationParentSessionID string `mapstructure:"delegation_parent_session_id"`
	DelegationProfile         string `mapstructure:"delegation_profile"`
	DelegationTimeout         int    `mapstructure:"delegation_timeout"`

	// TTS
	TTSProvider         string `mapstructure:"tts_provider"`
	TTSFallbackProvider string `mapstructure:"voice_fallback_provider"`
	TTSVoice            string `mapstructure:"tts_voice"`
	Audio8Voice         string `mapstructure:"audio8_voice"`
	// OpenAI-compatible speech client (Phonon via Ollama by default); see openaispeech.go.
	OpenAISpeechURL          string `mapstructure:"openai_speech_url"`
	OpenAISpeechModel        string `mapstructure:"openai_speech_model"`
	OpenAISpeechVoice        string `mapstructure:"openai_speech_voice"`
	OpenAISpeechAPIKey       string `mapstructure:"openai_speech_api_key"`
	OpenAISpeechTimeout      int    `mapstructure:"openai_speech_timeout"`
	OpenAISpeechRuntime      string `mapstructure:"openai_speech_runtime"`
	OpenAISpeechBackend      string `mapstructure:"openai_speech_backend"`
	OpenAISpeechNativeBinary string `mapstructure:"openai_speech_native_binary"`
	// Selection provenance is not saved or overlaid by a persona.
	OpenAISpeechExternal  bool    `mapstructure:"-"`
	SpeechSpeed           float64 `mapstructure:"speech_speed"`
	PlaybackVolume        float64 `mapstructure:"playback_volume"`
	QwenTTSBinary         string  `mapstructure:"qwen_tts_binary"`
	QwenTTSModel          string  `mapstructure:"qwen_tts_model"`
	QwenTTSTimeout        int     `mapstructure:"qwen_tts_timeout"`
	QwenTTSMode           string  `mapstructure:"qwen_tts_mode"`
	QwenTTSVoice          string  `mapstructure:"qwen_tts_voice"`
	QwenTTSLanguage       string  `mapstructure:"qwen_tts_language"`
	QwenTTSInstruction    string  `mapstructure:"qwen_tts_instruction"`
	QwenTTSReferenceAudio string  `mapstructure:"qwen_tts_reference_audio"`
	QwenTTSReferenceText  string  `mapstructure:"qwen_tts_reference_text"`
	QwenTTSConsent        bool    `mapstructure:"qwen_tts_consent"`
	// QwenTTSModelTier selects 0.6b (default) or 1.7b when the native package includes it.
	QwenTTSModelTier string `mapstructure:"qwen_tts_model_tier"`
	// QwenTTSStream decides whether a Stage B worker's PCM chunks are played as
	// they arrive (auto / on / off). See tts_stream.go.
	QwenTTSStream string `mapstructure:"qwen_tts_stream"`
	// QwenTTSWorkers is how many native worker subprocesses synthesize at
	// once. See qwen_workers.go QwenTTSWorkersOf.
	QwenTTSWorkers int `mapstructure:"qwen_tts_workers"`
	// QwenTTSNativeURL is the release tarball (https://… or file://…/…tar.gz) for models ensure.
	QwenTTSNativeURL string `mapstructure:"qwen_tts_native_url"`
	// QwenTTSNativeSHA256 is the optional sha256 of that compressed tarball.
	QwenTTSNativeSHA256 string `mapstructure:"qwen_tts_native_sha256"`
	OutputDevice        string `mapstructure:"output_device"`

	// Speech place is host topology, not a provider: tts_provider still names
	// the engine, tts_place says which computer runs it. Personas never store it.
	TTSPlace string `mapstructure:"tts_place"`
	// TTSWorkerURL addresses a speech-serve host when TTSPlace is lan.
	// https://host:7263 off-box; http:// only for loopback. Never carries the token.
	TTSWorkerURL string `mapstructure:"tts_worker_url"`
	// TTSWorkerToken authenticates the obey-speech/v1 hello.
	TTSWorkerToken string `mapstructure:"tts_worker_token"`
	// TTSLanStrict fails the agent instead of falling back to Kokoro when the
	// LAN speech host is unreachable.
	TTSLanStrict bool `mapstructure:"tts_lan_strict"`
	// TTSLanTimeoutMS bounds the dial and handshake, never the worker's model
	// load, which is seconds on a cold speech-serve.
	TTSLanTimeoutMS int `mapstructure:"tts_lan_timeout_ms"`
	// TTSLookahead is how many speech segments a reply may have queued but not
	// yet played. See tts_lookahead.go TTSLookaheadOf.
	TTSLookahead int `mapstructure:"tts_lookahead"`

	// STT
	STTProvider string `mapstructure:"stt_provider"`
	InputDevice string `mapstructure:"input_device"`
	STTMode     string `mapstructure:"stt_mode"`
	// STTCustomWords are spellings the transcriber restores after decoding:
	// "Samantha", "Claude=clawed,cloud" (aliases), "Ollama!" (fuzzy when the
	// word is rare). Config file only, like name_summon_aliases.
	STTCustomWords []string `mapstructure:"stt_custom_words"`
	// STTCustomWordsFromPersonas adds every persona display name to the list.
	STTCustomWordsFromPersonas bool `mapstructure:"stt_custom_words_from_personas"`
	// STTWordCorrectionThreshold is the fuzzy acceptance score; lower is stricter.
	STTWordCorrectionThreshold float64 `mapstructure:"stt_word_correction_threshold"`
	// STTFillerRemoval drops hesitation sounds (um, uh, hmm) from final transcripts.
	STTFillerRemoval bool `mapstructure:"stt_filler_removal"`
	// STTNumbers writes spoken numbers as digits where a reader would.
	STTNumbers bool `mapstructure:"stt_numbers"`
	// STTPostprocess* configure the optional post-processing model stage: its
	// own provider and model, never the brain's, so changing one never changes
	// the other. An empty model means the provider's configured model.
	STTPostprocessEnabled            bool    `mapstructure:"stt_postprocess_enabled"`
	STTPostprocessProvider           string  `mapstructure:"stt_postprocess_provider"`
	STTPostprocessModel              string  `mapstructure:"stt_postprocess_model"`
	STTPostprocessMode               string  `mapstructure:"stt_postprocess_mode"`
	STTPostprocessTimeoutMS          int     `mapstructure:"stt_postprocess_timeout_ms"`
	STTPostprocessKeepAlive          string  `mapstructure:"stt_postprocess_keep_alive"`
	STTPostprocessCandidateThreshold float64 `mapstructure:"stt_postprocess_candidate_threshold"`
	SherpaStreamingModel             string  `mapstructure:"sherpa_streaming_model"`
	WhisperModel                     string  `mapstructure:"whisper_model"`
	ParakeetModel                    string  `mapstructure:"parakeet_model"`
	WhisperQuantized                 bool    `mapstructure:"whisper_quantized"`
	WhisperCPPBinary                 string  `mapstructure:"whispercpp_binary"`
	WhisperCPPModel                  string  `mapstructure:"whispercpp_model"`
	WhisperCPPModelPath              string  `mapstructure:"whispercpp_model_path"`
	TranscribeCPPBinary              string  `mapstructure:"transcribecpp_binary"`
	TranscribeCPPModel               string  `mapstructure:"transcribecpp_model"`

	// VAD
	VADEnabled                bool    `mapstructure:"vad_enabled"`
	VADSilenceDuration        float64 `mapstructure:"vad_silence_duration"`
	EnvironmentContextEnabled bool    `mapstructure:"environment_context_enabled"`
	VADThreshold              float64 `mapstructure:"vad_threshold"`
	VADMinSpeechDuration      float64 `mapstructure:"vad_min_speech_duration"`
	// VADPreRollMS is how much audio captured just before the VAD confirms
	// speech is prepended to the recognized segment. The Silero VAD only marks
	// a segment once ~MinSpeechDuration of speech has accrued, so onset audio
	// (the first word) is otherwise clipped before STT ever sees it.
	VADPreRollMS int `mapstructure:"vad_pre_roll_ms"`

	// VoiceFrontend selects which AEC/NS/AGC chain runs on the mic before STT:
	// webrtc (apm build tag), nlms (built in), or off. See voice_frontend.go.
	VoiceFrontend string `mapstructure:"voice_frontend"`
	// VoiceFrontendNSLevel is APM's noise-suppression aggressiveness (0-3).
	// No effect on nlms or off.
	VoiceFrontendNSLevel int `mapstructure:"voice_frontend_ns_level"`
	// VoiceFrontendEnabled is the pre-migration key voice_frontend replaced
	// (D8): true -> nlms, false/absent -> off, applied by
	// resolveVoiceFrontendMigration only when voice_frontend is not itself
	// set. Kept so an old config.yaml keeps its behavior; new code should read
	// VoiceFrontendOf(cfg) instead of this field.
	VoiceFrontendEnabled bool `mapstructure:"voice_frontend_enabled"`

	// Barge-in (interrupt TTS when the user starts speaking). Off by default
	// until the echo-canceller is strong enough to avoid self-interruption.
	BargeInEnabled bool `mapstructure:"barge_in_enabled"`
	// BargeInVADThreshold is the Silero speech-confidence the barge-in VAD
	// requires before treating captured audio as the user interrupting
	// (0.3-0.95). See barge_in.go BargeInVADThresholdOf.
	BargeInVADThreshold float64 `mapstructure:"barge_in_vad_threshold"`
	// BargeInTailGuardMS is how long, in milliseconds, capture chunks are
	// skipped after playback pauses so trailing speaker echo cannot
	// accumulate toward a trip (200-800). See barge_in.go BargeInTailGuardOf.
	BargeInTailGuardMS int `mapstructure:"barge_in_tail_guard_ms"`
	// BargeInSpeakers is the S3 policy seam (D7): anyone (default) or
	// enrolled_only, which fuses the live speaker analyzer's verdict into the
	// interrupt controller. See barge_in_speakers.go BargeInSpeakersOf.
	BargeInSpeakers string `mapstructure:"barge_in_speakers"`

	// Talk hotkey: a system-wide key that opens and closes a spoken turn from
	// whatever app has focus. macOS and Windows register the combo; every other
	// OS reaches the same driver through `obey-voice talk`. See talk_hotkey.go
	// for the accessors, and internal/talkhotkey for the combo grammar and the
	// mode semantics.
	TalkHotkeyEnabled bool `mapstructure:"talk_hotkey_enabled"`
	// TalkHotkeyMode is hold, toggle or automatic — how a press maps onto a
	// turn. See talkhotkey.Driver.
	TalkHotkeyMode string `mapstructure:"talk_hotkey_mode"`
	// TalkHotkey is the combo itself, e.g. alt+space. Empty means the platform
	// default. See talkhotkey.ParseCombo and talkhotkey.DefaultComboFor.
	TalkHotkey string `mapstructure:"talk_hotkey"`

	// Name summon: a second way to open a hands-free turn, off by default
	// (D5). The gate is STT-side — a finalized transcript that does not start
	// with the agent's name is dropped before the brain sees it — so it costs
	// nothing until it is switched on. See internal/summon for the matching
	// rules and name_summon.go for the front-end it depends on.
	NameSummonEnabled bool `mapstructure:"name_summon_enabled"`
	// AcousticNameEnabled adds experimental Obey keyword evidence to Parakeet
	// conversation routing. It does not enable a wake gate or rewrite text.
	AcousticNameEnabled bool `mapstructure:"acoustic_name_enabled"`
	// NameSummonAliases are extra wake words beyond the persona display name
	// and its first word. Config-file only: no Settings row, and no environment
	// binding, for the same reason skills_disabled has none — a list is not
	// something an environment variable spells well.
	NameSummonAliases []string `mapstructure:"name_summon_aliases"`

	// TUIMouseEnabled lets the TUI claim the mouse so the wheel and trackpad
	// scroll the transcript. Claiming it means the terminal routes drags to
	// Obey Voice instead of its own selection, so copying transcript text needs a
	// modifier (option in iTerm2, fn in Terminal.app, shift in kitty). Turn this
	// off to get unmodified click-drag selection back and scroll with PgUp/PgDn.
	TUIMouseEnabled bool `mapstructure:"tui_mouse_enabled"`

	// Brain
	// PersonaRoutingTimeout bounds addressee inference, including model startup, in seconds.
	PersonaRoutingTimeout     int    `mapstructure:"persona_routing_timeout"`
	GroupParticipationEnabled bool   `mapstructure:"group_participation_enabled"`
	GroupParticipationMode    string `mapstructure:"group_participation_mode"`
	// HostedPersonaSharing is what a persona on a hosted backend receives of a
	// shared conversation: "from_invite" (default) or "whole_conversation".
	HostedPersonaSharing string `mapstructure:"hosted_persona_sharing"`

	BrainProvider      string `mapstructure:"brain_provider"`
	DecisionEnabled    bool   `mapstructure:"decision_enabled"`
	DecisionVoiceWork  bool   `mapstructure:"decision_voice_work"`
	DecisionTextWork   bool   `mapstructure:"decision_text_work"`
	DecisionAddressee  bool   `mapstructure:"decision_addressee"`
	DecisionProvider   string `mapstructure:"decision_provider"`
	DecisionRuntime    string `mapstructure:"decision_runtime"`
	DecisionDevice     string `mapstructure:"decision_device"`
	DecisionLocalModel string `mapstructure:"decision_local_model"`
	DecisionEndpoint   string `mapstructure:"decision_endpoint"`
	DecisionModel      string `mapstructure:"decision_model"`
	DecisionAPIKey     string `mapstructure:"decision_api_key"`
	DecisionBudgetMS   int    `mapstructure:"decision_budget_ms"`
	DecisionPolicy     string `mapstructure:"decision_policy"`
	// VoiceWorkEnabled keeps background outcomes with the conversation. This
	// experimental loop is opt-in; paid tiers require saved advisory uncapped consent.
	VoiceWorkEnabled bool `mapstructure:"voice_work_enabled"`

	// VoiceWorkersDisabled rejects new background jobs without stopping existing
	// work. This global preference is independent of the experimental inbox.
	VoiceWorkersDisabled bool `mapstructure:"voice_workers_disabled"`

	// VoiceWorkerProvider selects the async job worker brain (any supported brain provider).
	// Empty defaults to grok.
	VoiceWorkerProvider string `mapstructure:"voice_worker_provider"`
	// VoiceWorkerModel pins the worker model; empty inherits the provider default.
	VoiceWorkerModel string `mapstructure:"voice_worker_model"`
	// VoiceWorkerOllamaHost overrides ollama_host for the worker when it is ollama.
	VoiceWorkerOllamaHost string `mapstructure:"voice_worker_ollama_host"`
	// VoiceWorkerMaxConcurrent is how many spawned jobs run at once; the rest queue.
	VoiceWorkerMaxConcurrent int `mapstructure:"voice_worker_max_concurrent"`
	// VoiceWorkerJobBudget caps one spawned job's wall-clock seconds; zero means no cap.
	VoiceWorkerJobBudget int `mapstructure:"voice_worker_job_budget"`
	// VoiceWorkerBoundary fences a spawned worker to the campaign it was launched in.
	VoiceWorkerBoundary  bool   `mapstructure:"voice_worker_boundary"`
	GrokModel            string `mapstructure:"grok_model"`
	ClaudeModel          string `mapstructure:"claude_model"`
	CursorModel          string `mapstructure:"cursor_model"`
	MuseSparkModel       string `mapstructure:"muse_spark_model"`
	ObeyProvider         string `mapstructure:"obey_provider"`
	ObeyModel            string `mapstructure:"obey_model"`
	ObeyCamp             string `mapstructure:"obey_camp"`
	ObeySocket           string `mapstructure:"obey_socket"`
	ObeyToolPolicy       string `mapstructure:"obey_tool_policy"`
	CodexModel           string `mapstructure:"codex_model"`
	CodexReasoningEffort string `mapstructure:"codex_reasoning_effort"`
	OllamaModel          string `mapstructure:"ollama_model"`
	// OllamaEmbeddingModel routes user prompts to relevant Agent Skills. Empty
	// disables harness-side semantic activation and leaves model-driven
	// read_skill available as a fallback.
	OllamaEmbeddingModel      string  `mapstructure:"ollama_embedding_model"`
	SkillsSimilarityThreshold float64 `mapstructure:"skills_similarity_threshold"`
	OllamaHost                string  `mapstructure:"ollama_host"`
	// OllamaNumCtx is the context window requested on every chat call. 0
	// falls back to the server's model default — which silently truncates
	// long prompts from the top, eating the system prompt first.
	OllamaNumCtx int `mapstructure:"ollama_num_ctx"`
	// OllamaResponseTokens bounds one foreground response across tool rounds
	// and continuations. Zero uses the default; workers retain their own budgets.
	OllamaResponseTokens int `mapstructure:"ollama_response_tokens"`
	// VoiceWorkerNumCtx is the worker Chat window. 0 uses the worker model's
	// advertised context capacity, independently of the voice conversation.
	VoiceWorkerNumCtx int `mapstructure:"voice_worker_num_ctx"`
	// OllamaNumBatch is how many prompt tokens Ollama reads per pass. Left
	// unset, Ollama picks it mainly from the context window — 512 at or below
	// 4096, 1024 above, 2048 above 32768 — downgrading toward 512 when the
	// model's predicted VRAM will not fit. It weighs the model's memory
	// footprint, never its architecture. Values above OllamaNumCtx are clamped
	// to it at load; see ClampOllamaNumBatch.
	//
	// It is a prefix-REUSE control, not a speed control. llama.cpp keeps its
	// last reusable context checkpoint about one batch back from the end of the
	// prompt, so a smaller batch lets the server skip more of a conversational
	// prompt whose persona and history have not changed. Measured on
	// qwen3.8-uncensored:27b (B2): batch 1024 skips 14 % and waits 4.4 s before
	// the first word; batch 128 skips 89 % and waits 0.8 s. Raw reading speed
	// moves the other way (239 to 182 tok/s), so genuinely new prompts cost
	// slightly more. 0 restores Ollama's own choice.
	OllamaNumBatch int `mapstructure:"ollama_num_batch"`
	// OllamaKeepAlive keeps the model resident between voice turns
	// (Go duration string; empty = server default).
	OllamaKeepAlive string `mapstructure:"ollama_keep_alive"`
	// OllamaThink controls Ollama chat thinking/reasoning. Default "auto":
	// qwen3-family models get Think:true so chain-of-thought lands in
	// Message.Thinking, not spoken Content. Explicit false is the leaky hatch.
	// Values: auto, false|off|0, true|on|1, or low|medium|high|max.
	OllamaThink string `mapstructure:"ollama_think"`
	// ClaudeMaxSessionTokens caps the prompt the claude CLI replays on --resume.
	// Past it the brain starts a fresh CLI session from recent history instead of
	// resuming. 0 (the default) disables the cap and trusts the CLI session:
	// this is an opt-in fuse for unattended/serve setups, not the default
	// posture — ClaudeSessionWarnTokens is.
	ClaudeMaxSessionTokens int `mapstructure:"claude_max_session_tokens"`
	// ClaudeSessionWarnTokens is the replayed-prompt size at which the brain
	// surfaces a visible warning (log + activity feed), once per CLI session.
	// Nothing is dropped: the session keeps resuming. 0 disables the warning.
	ClaudeSessionWarnTokens int `mapstructure:"claude_session_warn_tokens"`

	VoiceToolsEnabled bool `mapstructure:"voice_tools_enabled"`
	// VoiceRouterTools selects the live-brain tool surface: route_only (default),
	// json_route (route_only decided by a tools-free JSON request on Ollama) or
	// legacy (full worker tool list for A/B).
	VoiceRouterTools string `mapstructure:"voice_router_tools"`
	// VoiceWorkNarration is how much a background worker narrates itself:
	// status_only (default) speaks only when asked, milestones speaks one short
	// line each time the worker moves from one kind of work to another.
	VoiceWorkNarration string `mapstructure:"voice_work_narration"`
	// VoiceFailedRouteRepair enables Layer 3 spawn repair when the model speaks
	// a tool promise with no tool_calls (design D3).
	VoiceFailedRouteRepair bool `mapstructure:"voice_failed_route_repair"`
	// ContinuityIntegrityEnabled detects truncated Ollama completions and
	// emits continuity observability events. False restores pre-WI-b94dad
	// behavior (no integrity flags or continuity events).
	ContinuityIntegrityEnabled  bool    `mapstructure:"continuity_integrity_enabled"`
	ContinuityProjectionEnabled bool    `mapstructure:"continuity_projection_enabled"`
	ContinuityArchiveEnabled    bool    `mapstructure:"continuity_archive_enabled"`
	ContinuitySoftLimitRatio    float64 `mapstructure:"continuity_soft_limit_ratio"`
	ContinuityCompactFiller     bool    `mapstructure:"continuity_compact_filler"`
	ContinuityRecoveryMax       int     `mapstructure:"continuity_recovery_max"`
	ContinuityNumCtxCeil        int     `mapstructure:"continuity_num_ctx_ceil"`

	// ToolCommandTimeout bounds one local run_command invocation in seconds.
	// The brain turn timeout remains the outer bound for a complete turn.
	ToolCommandTimeout int `mapstructure:"tool_command_timeout"`
	// RemoteToolsEnabled gates tool calls for turns triggered over the
	// network (obey-voice serve). Deliberately separate from
	// voice_tools_enabled: remote turns default-deny tools regardless of
	// the local flag.
	RemoteToolsEnabled bool `mapstructure:"remote_tools_enabled"`

	// Calibre (optional library catalog/resolver for audiobook input).
	// Off by default so non-users are unaffected.
	CalibreEnabled       bool   `mapstructure:"calibre_enabled"`
	CalibreLibraryPath   string `mapstructure:"calibre_library_path"`
	CalibredbBinary      string `mapstructure:"calibredb_binary"`
	CalibreConvertBinary string `mapstructure:"calibre_convert_binary"`
	CalibrePreferFormat  string `mapstructure:"calibre_prefer_format"`

	// Paths
	ModelsDir string `mapstructure:"models_dir"`

	// Agent
	AgentName string `mapstructure:"agent_name"`
	// UserDisplayName is the human's chosen name for prompts and live labels.
	// Empty keeps today's speaker-1 / $USER behaviour.
	UserDisplayName string `mapstructure:"user_display_name"`
	// RuntimeEnv* is an embedder-only grounding override. These values are not
	// loaded from or persisted to Obey Voice config; voiceagent.Options.Env writes
	// them onto its private config copy before constructing the brain.
	RuntimeEnvUser     string `mapstructure:"-"`
	RuntimeEnvHostname string `mapstructure:"-"`
	RuntimeEnvOS       string `mapstructure:"-"`

	// IsWorker marks the config WorkerBrainConfig hands to a spawned worker,
	// so a brain can tell a background job from the live conversation.
	// Runtime only, never persisted.
	IsWorker bool `mapstructure:"-"`

	// WorkerBoundary is the root a worker brain is fenced to, set by
	// WorkerBrainConfig from voice_worker_boundary. Empty on the live brain.
	// Runtime only: it is derived from the work directory, never persisted.
	WorkerBoundary string `mapstructure:"-"`

	// PersonaDisplayNames is every persona's display name, filled by the
	// persona after-load hook and the session binders so the STT factory can
	// spell them without importing the persona package. Not a config key.
	PersonaDisplayNames []string `mapstructure:"-"`

	// ActivePersona is the persona profile id under personas/<id>/.
	// On Load it drives agent_name, persona prompt name, and tts_voice overlays.
	ActivePersona string `mapstructure:"active_persona"`

	// Prompts
	Persona    string `mapstructure:"persona"`
	PromptsDir string `mapstructure:"prompts_dir"`

	// BackchannelEnabled plays a short filler ("Mm-hm") while a slow turn is
	// still being produced. Defaults to FALSE: it is a taste feature, and one
	// the plan expects may be removed after dogfooding.
	BackchannelEnabled bool `mapstructure:"backchannel_enabled"`
	// CompactPrompt names the kind=compact document used as /compact's
	// summarize-turn instruction. Empty selects the embedded default.
	CompactPrompt string `mapstructure:"compact_prompt"`

	// TurnPrompt is the catalog name for the kind=turn per-reply instruction.
	// Empty means the embedded shared default. Set at runtime by persona.Apply
	// from profile prompts.turn; not a user-facing top-level config key.
	TurnPrompt string `mapstructure:"-"`

	// Skills (Agent Skills / SKILL.md). On by default; set false to disable.
	// Ollama also re-enables on provider switch when the key is unset. See
	// internal/skills.
	SkillsEnabled bool   `mapstructure:"skills_enabled"`
	SkillsDir     string `mapstructure:"skills_dir"`
	// SkillsDisabled lists skill names turned off by the owner; the loader
	// omits them from the catalog at the single seam every delivery channel
	// (prompt menu, semantic router, read_skill) shares. The settings schema
	// exposes it as skills_disabled.
	SkillsDisabled []string `mapstructure:"skills_disabled"`

	// General
	// WorkDir is where the agent's file and shell tools start. It is a
	// default, not a sandbox — tools may still reach absolute paths.
	// Empty resolves through WorkDirFrom.
	// ConversationDirectories retains the app default and each responder’s directory.
	ConversationDirectories *ConversationDirectories `mapstructure:"-" json:"-"`
	// WorkDirSource is runtime-only provenance for the current responder.
	WorkDirSource   string `mapstructure:"-" json:"-"`
	WorkDir         string `mapstructure:"work_dir"`
	Language        string `mapstructure:"language"`
	MaxHistory      int    `mapstructure:"max_history"`
	ListenTimeout   int    `mapstructure:"listen_timeout"`
	PhraseTimeLimit int    `mapstructure:"phrase_time_limit"`

	// Meeting notes routing (post-record export to campaign/file/Apple Notes).
	Meeting MeetingConfig `mapstructure:"meeting"`

	// Speaker analysis (optional; diarization + embeddings via sherpa).
	// Nested under "speaker" — see internal/speaker.Config.
	Speaker SpeakerConfig `mapstructure:"speaker"`
}

// MeetingConfig holds meeting storage + routing preferences.
type MeetingConfig struct {
	// Dir overrides the default meetings directory when non-empty.
	Dir   string             `mapstructure:"dir"`
	Route MeetingRouteConfig `mapstructure:"route"`
}

// MeetingRouteConfig controls post-meeting note routing.
type MeetingRouteConfig struct {
	// Mode is ask | auto | off (default ask).
	Mode string `mapstructure:"mode"`
	// Default is the destination ID used by auto and preselected by ask.
	Default string `mapstructure:"default"`
	// Body is notes | full (default notes).
	Body string `mapstructure:"body"`
	// Destinations are named route targets (manage in YAML for v1).
	Destinations []MeetingDestinationConfig `mapstructure:"destinations"`
}

// MeetingDestinationConfig is one named export target.
//
// The yaml tags matter only on the way out: `config set` writes a destination
// list back through the YAML encoder, and without omitempty every entry would
// carry the fields its type does not use (tags: [], path: "", folder: "") into
// the user's file. Reading still goes through viper and the mapstructure tags.
type MeetingDestinationConfig struct {
	ID       string   `mapstructure:"id" yaml:"id"`
	Type     string   `mapstructure:"type" yaml:"type"` // campaign | file | apple-notes
	Campaign string   `mapstructure:"campaign" yaml:"campaign,omitempty"`
	Capture  string   `mapstructure:"capture" yaml:"capture,omitempty"` // meeting | intent | note (default meeting → import-meeting)
	Tags     []string `mapstructure:"tags" yaml:"tags,omitempty"`
	Path     string   `mapstructure:"path" yaml:"path,omitempty"`
	Folder   string   `mapstructure:"folder" yaml:"folder,omitempty"`
}

// SpeakerConfig mirrors speaker.Config for viper unmarshal (keeps config package
// free of importing speaker for package cycles).
type SpeakerConfig struct {
	Enabled       bool                 `mapstructure:"enabled"`
	Threshold     float32              `mapstructure:"threshold"`
	EnrollmentDir string               `mapstructure:"enrollment_dir"`
	Live          SpeakerLiveConfig    `mapstructure:"live"`
	Meeting       SpeakerMeetingConfig `mapstructure:"meeting"`
	Models        SpeakerModelsConfig  `mapstructure:"models"`
}

// SpeakerLiveConfig is the async conversation speaker path.
type SpeakerLiveConfig struct {
	Enabled   bool    `mapstructure:"enabled"`
	Mode      string  `mapstructure:"mode"`
	Threshold float32 `mapstructure:"threshold"`
	WindowMS  int     `mapstructure:"window_ms"`
}

// SpeakerMeetingConfig is offline meeting diarization (not notes routing).
type SpeakerMeetingConfig struct {
	Enabled     bool `mapstructure:"enabled"`
	RecordAudio bool `mapstructure:"record_audio"`
	NumSpeakers int  `mapstructure:"num_speakers"`
	// Live enables provisional speaker-N labels on the live transcript while
	// recording (embedding path). Offline diarization after stop is separate.
	Live bool `mapstructure:"live"`
	// Diarizer selects the engine behind the offline review pass; see
	// MeetingDiarizers. Empty is the default sherpa path.
	Diarizer string `mapstructure:"diarizer"`
}

// SpeakerModelsConfig holds optional model paths under models_dir.
type SpeakerModelsConfig struct {
	Embedding    string `mapstructure:"embedding"`
	Segmentation string `mapstructure:"segmentation"`
}
