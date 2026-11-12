package config

import "github.com/Obedience-Corp/obey-voice/internal/talkhotkey"

// This file holds the key specs for the groups Settings renders itself.
// schema_keys_models.go holds the rest. Titles are sentence case and name the
// thing rather than the key; help is one sentence stating the consequence.

func voiceKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "tts_provider", Type: TypeEnum, Default: "kokoro", Enum: ttsProviderEnum(),
			Group: GroupVoice, Title: "Speech engine",
			Help:               "Which engine speaks. Kokoro is fastest; Qwen sounds richer and costs latency.",
			PersonaOverridable: true,
		},
		{
			Key: "tts_place", Type: TypeEnum, Default: TTSPlaceLocal, Enum: ttsPlaceEnum(),
			Group: GroupVoice, Title: "Where speech runs",
			Help: "Whether Qwen speaks on this computer or on another one running speech-serve. Kokoro always speaks here.",
		},
		{
			Key: "tts_worker_url", Type: TypeString, Default: "",
			Group: GroupVoice, Title: "Speech computer address",
			Help: "The speech-serve host to send text to, like https://studio:7263. Only used when speech runs on another computer.",
		},
		{
			Key: "tts_worker_token", Type: TypeString, Default: "", Secret: true,
			Group: GroupVoice, Title: "Speech computer token",
			Help: "The shared secret that speech computer requires. Empty falls back to the OBEY_SPEECH_TOKEN environment variable.",
		},
		{
			Key: "tts_lan_strict", Type: TypeBool, Default: false,
			Group: GroupVoice, Title: "Require the speech computer",
			Help: "Refuses to start a conversation when the speech computer is unreachable, instead of speaking in the fallback voice.",
		},
		{
			Key: "tts_lan_timeout_ms", Type: TypeInt, Default: DefaultTTSLanTimeoutMS, Unit: "ms", Min: num(50), Max: num(10000),
			Group: GroupVoice, Title: "Speech computer timeout",
			Help: "How long to wait for the speech computer to answer a connection. It does not bound the model load on a cold host.",
		},
		{
			Key: "tts_lookahead", Type: TypeInt, Default: DefaultTTSLookahead, Unit: "segments", Min: num(MinTTSLookahead), Max: num(MaxTTSLookahead),
			Group: GroupVoice, Title: "Speech lookahead",
			Help: "How many pieces of a reply may be prepared ahead of the one being spoken. 2 keeps one playing and one being made. Higher only helps an engine that can make more than one at a time.",
		},
		{
			Key: "tts_voice", Type: TypeEnum, Default: DefaultKokoroVoice, Enum: kokoroVoiceEnum(),
			Group: GroupVoice, Title: "Kokoro voice",
			Help:               "The voice Kokoro speaks in. Qwen ignores this and uses its own preset.",
			PersonaOverridable: true,
		},
		{
			// Not an enum, unlike every other voice key: an audio8 voice is a
			// name the user chose when they registered a reference recording,
			// so the valid set lives in the voice store on disk, not in this
			// binary. `config set` therefore accepts any well-formed name and
			// the provider reports an unknown one when it goes to speak.
			Key: "audio8_voice", Type: TypeString, Default: "",
			Group: GroupVoice, Title: "Audio8 voice",
			Help:               "Which registered Audio8 voice speaks. Empty uses the voice packaged with the export. Register more with `obey-voice voices register`.",
			PersonaOverridable: true,
		},
		{
			Key: "openai_speech_url", Type: TypeString, Default: DefaultOpenAISpeechURL,
			Group: GroupVoice, Title: "Speech API address",
			Help: "The OpenAI-compatible /v1 base the openai-speech engine talks to. Ollama serving Phonon is http://localhost:11434/v1.",
		},
		{
			Key: "openai_speech_runtime", Type: TypeEnum, Default: "auto", Enum: []string{"auto", "external", "native"},
			Group: GroupVoice, Title: "Speech runtime",
			Help: "Auto uses managed Phonon on Linux when no server/model was explicitly configured, and Ollama on macOS. External preserves the configured server; native owns a local Phonon process.",
		},
		{
			Key: "openai_speech_backend", Type: TypeEnum, Default: "auto", Enum: []string{"auto", "cpu", "vulkan", "cuda", "metal"},
			Group: GroupVoice, Title: "Native speech backend",
			Help: "Device for managed native Phonon. Auto chooses CUDA on NVIDIA, Vulkan on supported Linux ICDs, otherwise CPU. An unavailable explicit device fails rather than silently falling back.",
		},
		{
			Key: "openai_speech_native_binary", Type: TypeString, Default: "",
			Group: GroupVoice, Title: "Native speech binary",
			Help: "Optional absolute path to a compatible ptts-openai-server. Empty builds the pinned native runtime during model setup.",
		},
		{
			Key: "openai_speech_model", Type: TypeString, Default: DefaultOpenAISpeechModel,
			Group: GroupVoice, Title: "Speech API model",
			Help: "The model id the speech API synthesizes with. phonon on Ollama; `ollama pull phonon` installs it.",
		},
		{
			// A string, like audio8_voice: the valid set lives on the server, which
			// names the voices it has when asked for one it lacks.
			Key: "openai_speech_voice", Type: TypeString, Default: DefaultOpenAISpeechVoice,
			Group: GroupVoice, Title: "Speech API voice",
			Help:               "Which voice the speech API speaks in. Phonon on Ollama 0.40.11+ offers named voices after voice setup; default remains available.",
			PersonaOverridable: true,
		},
		{
			Key: "openai_speech_api_key", Type: TypeString, Default: "", Secret: true,
			Group: GroupVoice, Title: "Speech API key",
			Help: "Bearer token for a speech API that needs one. Ollama does not.",
		},
		{
			Key: "openai_speech_timeout", Type: TypeInt, Default: DefaultOpenAISpeechTimeout, Unit: "s", Min: num(5), Max: num(600),
			Group: GroupVoice, Title: "Speech API timeout",
			Help: "How long one speech request may take before it is abandoned.",
		},
		{
			Key: "voice_fallback_provider", Type: TypeEnum, Default: "kokoro", Enum: ttsFallbackEnum(),
			Group: GroupVoice, Title: "Fallback engine",
			Help: "When the main engine is slow or fails, speak that sentence in this engine instead. disabled waits for the main engine (you hear Qwen, not Kokoro).",
		},
		{
			Key: "speech_speed", Type: TypeFloat, Default: 0.95, Unit: "×", Min: num(0.5), Max: num(2.0),
			Group: GroupVoice, Title: "Speaking rate",
			Help: "How fast she talks. Above about 1.3 the words start to slur.",
		},
		{
			Key: "playback_volume", Type: TypeFloat, Default: DefaultPlaybackVolume, Unit: "×", Min: num(MinPlaybackVolume), Max: num(MaxPlaybackVolume),
			Group: GroupVoice, Title: "Volume",
			Help: "How loud she is. 1 is normal; 0 is silent; above 1 boosts a quiet speaker.",
		},
		{
			Key: "output_device", Type: TypeString, Default: "",
			Group: GroupVoice, Title: "Speaker",
			Help: "Where her voice plays. Empty follows the system output device.",
		},
		{
			Key: "input_device", Type: TypeString, Default: "",
			Group: GroupVoice, Title: "Microphone",
			Help: "Which microphone she listens to. Empty follows the system input device.",
		},
		{
			Key: "vad_enabled", Type: TypeBool, Default: true,
			Group: GroupVoice, Title: "Detect speech automatically",
			Help: "Ends your turn when you stop talking. Off means every turn waits for the listen timeout.",
		},
		{
			Key: "vad_silence_duration", Type: TypeFloat, Default: 1.2, Unit: "seconds", Min: num(0.1), Max: num(3.0),
			Group: GroupVoice, Title: "End-of-turn silence",
			Help: "Seconds of quiet that end your turn. Lower feels snappier; too low cuts you off mid-sentence.",
		},
		{
			Key: "vad_threshold", Type: TypeFloat, Default: 0.6, Min: num(0.0), Max: num(1.0),
			Group: GroupVoice, Title: "Speech confidence",
			Help: "How sure she must be that a sound is speech. Raise it in a noisy room; too high drops quiet talkers.",
		},
		{
			Key: "vad_min_speech_duration", Type: TypeFloat, Default: 0.1, Unit: "seconds", Min: num(0.05), Max: num(2.0),
			Group: GroupVoice, Title: "Shortest utterance",
			Help: "Minimum speech duration. Lower values keep brief replies such as yes or no; audible-speech and noise checks still apply.",
		},
		{
			Key: "vad_pre_roll_ms", Type: TypeInt, Default: 300, Unit: "ms", Min: num(0), Max: num(2000),
			Group: GroupVoice, Title: "Pre-speech audio",
			Help: "Audio kept from just before speech is detected, so your first word isn't clipped.",
		},
		{
			Key: "voice_frontend", Type: TypeEnum, Default: VoiceFrontendOff, Enum: voiceFrontendEnum(),
			Group: GroupVoice, Title: "Voice front-end",
			Help: "Runs echo cancellation on this Mac's microphone before speech detection. webrtc needs a build with the apm tag; nlms is the built-in fallback. Required for barge-in.",
		},
		{
			Key: "voice_frontend_ns_level", Type: TypeInt, Default: DefaultVoiceFrontendNSLevel, Min: num(0), Max: num(3),
			Group: GroupVoice, Title: "Noise suppression level",
			Help: "How aggressively the webrtc front-end suppresses background noise, from 0 (off) to 3 (aggressive). No effect on nlms or off.",
		},
		{
			Key: "barge_in_enabled", Type: TypeBool, Default: false,
			Group: GroupVoice, Title: "Barge-in",
			Help: "Lets you talk over her. Needs the voice front-end on, and only affects this Mac's microphone — phone clients still need the interrupt button.",
		},
		{
			Key: "barge_in_vad_threshold", Type: TypeFloat, Default: DefaultBargeInVADThreshold, Min: num(MinBargeInVADThreshold), Max: num(MaxBargeInVADThreshold),
			Group: GroupVoice, Title: "Barge-in speech confidence",
			Help: "How sure the barge-in detector must be that mic audio is real speech before cutting her off. voice_frontend=webrtc defaults to 0.6; nlms/off stay at 0.85 unless you set this.",
		},
		{
			Key: "barge_in_tail_guard_ms", Type: TypeInt, Default: DefaultBargeInTailGuardMS, Unit: "ms", Min: num(MinBargeInTailGuardMS), Max: num(MaxBargeInTailGuardMS),
			Group: GroupVoice, Title: "Barge-in echo tail guard",
			Help: "How long after playback pauses to ignore the mic, so speaker echo trailing off doesn't trip a false barge-in.",
		},
		{
			Key: "barge_in_speakers", Type: TypeEnum, Default: BargeInSpeakersAnyone, Enum: bargeInSpeakersEnum(),
			Group: GroupVoice, Title: "Who can interrupt",
			Help: "enrolled_only requires a fresh enrolled-voice match. Automatic voice interruptions are blocked while recognition is off, cold, stale, or unhealthy. Typing can still interrupt.",
		},
		{
			Key: "talk_hotkey_enabled", Type: TypeBool, Default: true,
			Group: GroupVoice, Title: "Talk hotkey",
			Help: "Lets a system-wide key start and end a spoken turn from any app. macOS and Windows register the combo themselves; elsewhere bind your shortcut tool to `obey-voice talk`. Ctrl+G stays the mute.",
		},
		{
			Key: "talk_hotkey_mode", Type: TypeEnum, Default: string(talkhotkey.DefaultMode), Enum: talkhotkey.Modes(),
			Group: GroupVoice, Title: "Talk hotkey mode",
			Help: "hold = press to talk, release to send; toggle = tap to start, tap to send; automatic = tap toggles, holding longer than 300 ms is a hold.",
		},
		{
			Key: "talk_hotkey", Type: TypeString, Default: "",
			Group: GroupVoice, Title: "Talk hotkey combo",
			Help: "Modifiers plus a key, like ctrl+space or ctrl+alt+t. Needs at least one of ctrl, alt or cmd, or a bare function key. Empty uses the platform default: Ctrl+Space on macOS (the Obey Voice Mac app's key), Ctrl+Alt+Space on Windows; on Linux the key is whatever you bind to `obey-voice talk`. Also in Settings → Tools.",
		},
		{
			Key: "name_summon_enabled", Type: TypeBool, Default: false,
			Group: GroupVoice, Title: "Name summon",
			Help: "With the mic open, only a turn that starts with the agent's name or an alias is answered; the talk key bypasses it. Needs the voice front-end on, so playback of the name cannot summon the agent.",
		},
		{
			Key: "acoustic_name_enabled", Type: TypeBool, Default: false,
			Group: GroupVoice, Title: "Acoustic Obey recognition (experimental)",
			Help: "Adds an acoustic name hint to semantic conversation routing when Parakeet mishears Obey. Requires Parakeet and models ensure --stt; leaves the transcript unchanged. Not a wake gate. Edited in config.yaml.",
		},
		{
			Key: "name_summon_aliases", Type: TypeStringList, Default: []string{},
			Group: GroupVoice, Title: "Name summon aliases",
			Help: "Extra wake words besides the agent's display name and its first word — \"computer\", say. Edited in config.yaml; Settings has no row for it.",
		},
		{
			Key: "backchannel_enabled", Type: TypeBool, Default: false,
			Group: GroupVoice, Title: "Thinking sounds",
			Help: "Plays a short \"mm-hm\" while a slow answer is still coming, so the pause doesn't feel dead.",
		},
	}
}

func brainKeys() []KeySpec {
	return []KeySpec{
		{Key: "group_participation_enabled", Type: TypeBool, Default: false, Group: GroupBrain, Title: "Group participation (experimental)", Help: "Allow contextual group invitations on local Ollama. Requires a model that reliably distinguishes invitations from mentions. Applies to the next conversation."},
		{Key: "group_participation_mode", Type: TypeEnum, Default: "invited", Enum: []string{"off", "invited", "open"}, Group: GroupBrain, Title: "Agent participation", Help: "Invited allows group discussion when asked. Open additionally allows one useful contribution after an ordinary reply. Applies to new conversations; /participation changes the current conversation."},
		{Key: "hosted_persona_sharing", Type: TypeEnum, Default: "from_invite", Enum: []string{"from_invite", "whole_conversation"}, Group: GroupBrain, Title: "Hosted persona sharing", Help: "What a persona on Claude, Codex, Muse Spark or Obey receives of a conversation shared with other personas; a persona on Grok or Cursor only holds a conversation started on it. from_invite sends only what is said from when the persona is first addressed, plus the task and result of each background job it started earlier, on any backend. whole_conversation sends the spoken lines from the start; tool calls, tool output and attachment names are never sent. Read when a persona is first addressed; a running conversation applies a change only after settings reload, and only to personas first addressed afterwards."},
		{
			Key: "brain_provider", Type: TypeEnum, Default: "ollama", Enum: brainProviderEnum(),
			Group: GroupBrain, Title: "Model provider",
			Help:               brainProviderHelp(),
			PersonaOverridable: true,
		},
		{
			Key: "persona_routing_timeout", Type: TypeInt, Default: DefaultPersonaRoutingTimeout, Unit: "seconds", Min: num(1), Max: num(MaxPersonaRoutingTimeout),
			Group: GroupBrain, Title: "Persona routing timeout",
			Help: "How long to wait while deciding which persona should answer, including model startup. Lower this to report slow routing sooner.",
		},
		{
			Key: "voice_work_enabled", Type: TypeBool, Default: false,
			Group: GroupAdvanced, Title: "Experimental work inbox",
			Help: "Keep background results with the conversation and save pending updates. Experimental; paid worker dispatch is unavailable.",
		},
		{
			Key: "voice_workers_disabled", Type: TypeBool, Default: false,
			Group: GroupBrain, Title: "Disable background workers",
			Help: "Reject new background jobs. Existing running and queued jobs may finish; use /workers to stop them. The TUI applies it live; serve reads it when it builds the pipeline.",
		},
		{
			Key: "voice_worker_provider", Type: TypeEnum, Default: "", Enum: workerProviderEnum(),
			Group: GroupBrain, Title: "Worker provider",
			Help:               "Which model runs async jobs after a spawn. Chosen alongside the brain — empty means unselected, and inherits the live brain until you pick one.",
			PersonaOverridable: true,
		},
		{
			Key: "voice_worker_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Worker model",
			Help:               "Model pin for the async worker. Empty uses that provider's usual default.",
			PersonaOverridable: true,
		},
		{
			Key: "voice_worker_ollama_host", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Worker Ollama server",
			Help:               "Ollama base URL for the async worker when voice_worker_provider is ollama. Empty inherits ollama_host.",
			PersonaOverridable: true,
		},
		{
			Key: "voice_worker_max_concurrent", Type: TypeInt, Default: DefaultVoiceWorkerMaxConcurrent, Min: num(1), Max: num(8),
			Group: GroupBrain, Title: "Jobs at once",
			Help: "How many spawned jobs run at the same time; the rest wait in order. Each is its own model session, so more than one on a local Ollama shares the machine.",
		},
		{
			Key: "voice_worker_job_budget", Type: TypeInt, Default: DefaultVoiceWorkerJobBudget, Unit: "seconds", Min: num(0), Max: num(86400),
			Group: GroupBrain, Title: "Job time budget",
			Help: "Optional limit on how long a spawned job may run before it is stopped and you are told. Zero, the default, means no limit.",
		},
		{
			Key: "voice_worker_boundary", Type: TypeBool, Default: true,
			Group: GroupBrain, Title: "Work boundary",
			Help: "Keeps a spawned worker inside the directory it was launched in, or that directory's campaign when it is inside one: its file tools and shell refuse paths outside it, and its prompt says so.",
		},
		{
			Key: "ollama_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Ollama model",
			Help:               "The local model Ollama loads. Empty picks the first installed model.",
			PersonaOverridable: true,
		},
		{
			Key: "grok_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Grok model",
			Help:               "The model the Grok CLI is asked for. Empty uses the CLI's own default.",
			PersonaOverridable: true,
		},
		{
			Key: "claude_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Claude model",
			Help:               "The model the Claude CLI is asked for, such as sonnet, opus or haiku. Empty uses the CLI's own default.",
			PersonaOverridable: true,
		},
		{
			Key: "obey_provider", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Daemon provider",
			Help: "Provider name configured in the Obey daemon. Required when brain_provider is obey.",
		},
		{
			Key: "obey_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Daemon model",
			Help:               "Model served by the selected Obey daemon provider. Required when brain_provider is obey.",
			PersonaOverridable: true,
		},
		{
			Key: "obey_camp", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Daemon camp",
			Help: "Obey camp name or ID. Empty lets the CLI infer it from the working directory; explicit camp enables live events on compatible daemons.",
		},
		{
			Key: "obey_socket", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Daemon socket",
			Help: "Obey daemon socket path. Empty uses the installed CLI configuration.",
		},
		{
			Key: "obey_tool_policy", Type: TypeEnum, Default: "strict", Enum: []string{"strict", "daemon"},
			Group: GroupBrain, Title: "Daemon tool policy",
			Help: "Strict refuses tools-off turns because Obey has no universal tools-off control. Daemon delegates tool permissions to Obey, including conversational turns; discussion mode may still use tools.",
		},
		{
			Key: "codex_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Codex model",
			Help:               "The model used by Codex. Empty uses the Codex default. Requires Codex installed and signed in with codex login.",
			PersonaOverridable: true,
		},
		{
			Key: "codex_reasoning_effort", Type: TypeEnum, Default: "",
			Enum:  []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
			Group: GroupBrain, Title: "Codex reasoning effort",
			Help: "How much reasoning Codex uses. Empty uses its default. Supported efforts depend on the selected model; higher effort can delay spoken replies.",
		},
		{
			Key: "muse_spark_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Muse Spark model",
			Help:               "The model Muse is asked for. Empty uses the CLI default. Requires Muse installed and authenticated.",
			PersonaOverridable: true,
		},
		{
			Key: "cursor_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Cursor model",
			Help:               "The model cursor-agent is asked for. Empty uses the CLI's own default.",
			PersonaOverridable: true,
		},
		{
			Key: "ollama_host", Type: TypeString, Default: "http://localhost:11434",
			Group: GroupBrain, Title: "Ollama server",
			Help: "Where Ollama is running. Point it at another machine to answer from there.",
		},
		{
			Key: "ollama_num_ctx", Type: TypeInt, Default: 32768, Unit: "tokens", Min: num(0), Max: num(131072),
			Group: GroupBrain, Title: "Conversation memory",
			Help: "How much of this conversation the talk model sees (8k / 16k / 32k). 32k is a real session. 0 uses the server's size and can drop the persona from the top.",
		},
		{
			Key: "ollama_response_tokens", Type: TypeInt, Default: 4096, Unit: "tokens", Min: num(1), Max: num(131072),
			Group: GroupBrain, Title: "Maximum response size",
			Help: "Limits a foreground Ollama response, including reasoning and tool rounds. Stops runaway generation without automatically continuing. Workers use their own budgets.",
		},
		{
			Key: "ollama_num_batch", Type: TypeInt, Default: 128, Unit: "tokens", Min: num(0), Max: num(4096),
			Group: GroupBrain, Title: "Prompt batch size",
			Help: "How much of the prompt is re-read each turn. Smaller lets Ollama reuse more of an unchanged conversation, which is a large win to the first word on qwen3.8-class models, and costs a little on brand-new prompts. 0 lets Ollama choose.",
		},
		{
			Key: "ollama_keep_alive", Type: TypeString, Default: "10m",
			Group: GroupBrain, Title: "Model residency",
			Help: "How long Ollama keeps the model in memory between turns. Shorter frees RAM but pays the load cost again.",
		},
		{
			Key: "ollama_think", Type: TypeEnum, Default: "auto", Enum: ollamaThinkEnum(),
			Group: GroupBrain, Title: "Reasoning",
			Help: "auto/false leave thinking off so greetings are not a minute of silent CoT. Set true or low/medium/high/max to enable it.",
		},
		{
			Key: "claude_max_session_tokens", Type: TypeInt, Default: 0, Unit: "tokens", Min: num(0), Max: num(1000000),
			Group: GroupBrain, Title: "Claude session cap",
			Help: "Starts a fresh Claude session once the replayed prompt passes this size. 0 keeps resuming forever.",
		},
		{
			Key: "claude_session_warn_tokens", Type: TypeInt, Default: 60000, Unit: "tokens", Min: num(0), Max: num(1000000),
			Group: GroupBrain, Title: "Claude session warning",
			Help: "Warns once when a Claude session's replayed prompt passes this size. Nothing is dropped. 0 disables the warning.",
		},
		{
			Key: "environment_context_enabled", Type: TypeBool, Default: true,
			Group: GroupBrain, Title: "Machine context",
			Help: "Tells the model your user name, host and OS so it can answer questions about this machine.",
		},
		{
			Key: "max_history", Type: TypeInt, Default: 40, Min: num(1), Max: num(100),
			Group: GroupBrain, Title: "Remembered turns",
			Help: "Turn-count backstop for compact. The token window is what actually limits a long chat.",
		},
		{
			Key: "continuity_integrity_enabled", Type: TypeBool, Default: true,
			Group: GroupBrain, Title: "Context integrity checks",
			Help: "Detect truncated Ollama completions and emit continuity events. Off restores pre-continuity behavior.",
		},
		{Key: "continuity_projection_enabled", Type: TypeBool, Default: true, Group: GroupBrain, Title: "Compact tool results", Help: "Bound model-visible Ollama tool results to 2048 bytes. Disabling restores raw output."},
		{Key: "continuity_archive_enabled", Type: TypeBool, Default: true, Group: GroupBrain, Title: "Context archive", Help: "Retain bounded tool output and evicted notes in session memory."},
		{Key: "continuity_soft_limit_ratio", Type: TypeFloat, Default: 0.80, Min: num(0), Max: num(0.9), Group: GroupBrain, Title: "Context compact threshold", Help: "Prompt estimate fraction that triggers compaction. Zero disables the budget trigger."},
		{Key: "continuity_compact_filler", Type: TypeBool, Default: false, Group: GroupBrain, Title: "Speak before compacting", Help: "Say a brief filler before automatic conversation compaction. Off by default; the TUI meter is the signal."},
		{Key: "continuity_recovery_max", Type: TypeInt, Default: 2, Min: num(0), Max: num(2), Group: GroupBrain, Title: "Continuation attempts", Help: "Maximum continuation requests after a truncated Ollama response. Zero disables recovery."},
		{Key: "continuity_num_ctx_ceil", Type: TypeInt, Default: 32768, Min: num(8192), Max: num(32768), Group: GroupBrain, Title: "Conversation memory ceiling", Help: "Highest talk window under pressure. Keep equal to conversation memory unless you start smaller and want it to grow."},
		{Key: "voice_worker_num_ctx", Type: TypeInt, Default: 0, Unit: "tokens", Min: num(0), Group: GroupBrain, Title: "Worker memory", Help: "Context window for Ollama background jobs. 0 uses the selected worker model's advertised capacity (128k if unavailable). Set a token count to override it. Workers compact automatically at the continuity soft limit, independently of conversation memory."},
	}
}

func speakerKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "user_display_name", Type: TypeString, Default: "",
			Group: GroupSpeakers, Title: "Your name",
			Help: "The name the agent uses for you in prompts and chat labels instead of speaker-1. Empty keeps the operating-system user and anonymous labels.",
		},
		{
			Key: "speaker.enabled", Type: TypeBool, Default: true,
			Group: GroupSpeakers, Title: "Speaker recognition",
			Help: "Master switch. Off means no speaker models load and nobody is ever labelled.",
		},
		{
			Key: "speaker.threshold", Type: TypeFloat, Default: 0.6, Min: num(0.0), Max: num(1.0),
			Group: GroupSpeakers, Title: "Match confidence",
			Help: "How close a voice must be to an enrolled profile to count as that person. Higher misses more; lower mislabels more.",
		},
		{
			Key: "speaker.enrollment_dir", Type: TypeString, Default: "",
			Group: GroupSpeakers, Title: "Enrolled voices folder",
			Help: "Where enrolled voice profiles are stored. Empty uses the folder inside Obey Voice's install root.",
		},
		{
			Key: "speaker.live.enabled", Type: TypeBool, Default: true,
			Group: GroupSpeakers, Title: "Live speaker labels",
			Help: "Labels who is speaking during a conversation, not just afterwards in meetings.",
		},
		{
			Key: "speaker.live.mode", Type: TypeEnum, Default: "indicator", Enum: []string{"indicator", "owner_verify"},
			Group: GroupSpeakers, Title: "Live label mode",
			Help: "Indicator only shows who is talking; owner-verify also ignores turns from anyone but you when it is confident.",
		},
		{
			Key: "speaker.live.threshold", Type: TypeFloat, Default: 0.0, Min: num(0.0), Max: num(1.0),
			Group: GroupSpeakers, Title: "Live match confidence",
			Help: "Match cutoff for live labels. 0 inherits the general match confidence.",
		},
		{
			Key: "speaker.live.window_ms", Type: TypeInt, Default: 1500, Unit: "ms", Min: num(500), Max: num(10000),
			Group: GroupSpeakers, Title: "Live analysis window",
			Help: "Must match the window used when speakers were enrolled — a mismatch roughly doubles missed matches.",
		},
	}
}

func meetingKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "meeting.dir", Type: TypeString, Default: "",
			Group: GroupMeetings, Title: "Meetings folder",
			Help: "Where recorded meetings and their notes are written. Empty uses the folder inside Obey Voice's install root.",
		},
		{
			Key: "meeting.route.mode", Type: TypeEnum, Default: "ask", Enum: []string{"ask", "auto", "off"},
			Group: GroupMeetings, Title: "Export notes",
			Help: "Ask prompts after every meeting, auto exports straight to the default destination, off never exports.",
		},
		{
			Key: "meeting.route.default", Type: TypeEnum, Default: "",
			Group: GroupMeetings, Title: "Default destination",
			Help: "The destination auto exports to, and the one preselected when she asks.",
		},
		{
			Key: "meeting.route.body", Type: TypeEnum, Default: "full", Enum: []string{"notes", "full"},
			Group: GroupMeetings, Title: "Exported body",
			Help: "Both options include the full transcript in what gets exported.",
		},
		{
			Key: "meeting.route.destinations", Type: TypeOpaque, Default: []any{},
			Group: GroupMeetings, Title: "Route destinations",
			Help: "Named export targets for meeting notes.", ManagedBy: "meeting destinations",
		},
		{
			Key: "speaker.meeting.enabled", Type: TypeBool, Default: true,
			Group: GroupMeetings, Title: "Identify speakers in meetings",
			Help: "Splits a finished recording by speaker so the transcript says who said what.",
		},
		{
			Key: "speaker.meeting.record_audio", Type: TypeBool, Default: false,
			Group: GroupMeetings, Title: "Keep meeting audio",
			Help: "Keeps the meeting audio file. Without it, speakers can't be re-analysed after the meeting ends.",
		},
		{
			Key: "speaker.meeting.num_speakers", Type: TypeInt, Default: 0, Min: num(0), Max: num(20),
			Group: GroupMeetings, Title: "Expected speakers",
			Help: "Fixes how many people the analysis splits the room into. 0 lets it work that out.",
		},
		{
			Key: "speaker.meeting.live", Type: TypeBool, Default: true,
			Group: GroupMeetings, Title: "Label while recording",
			Help: "Shows provisional speaker labels during the meeting; the final pass still corrects them.",
		},
		{
			Key: "speaker.meeting.diarizer", Type: TypeEnum, Default: DefaultMeetingDiarizer, Enum: MeetingDiarizers(),
			Group: GroupMeetings, Title: "Diarization engine",
			Help: "Which model splits a finished recording by speaker. Sortformer is optional, needs the transcribe.cpp CLI plus its weights, and ignores the expected-speaker count.",
		},
	}
}

func toolKeys() []KeySpec {
	return append(delegationKeys(), []KeySpec{
		{
			Key: "voice_tools_enabled", Type: TypeBool, Default: true,
			Group: GroupTools, Title: "Local tools",
			Help: "Lets her read files and run commands on this Mac for you. Off makes tool-capable models look broken.",
		},
		{
			Key: "voice_router_tools", Type: TypeEnum, Default: "route_only", Enum: []string{"route_only", "legacy"},
			Group: GroupTools, Title: "Live router tools",
			Help: "route_only exposes only the route tool on the microphone brain; legacy keeps the full tool list for comparison.",
		},
		{
			Key: "voice_work_narration", Type: TypeEnum, Default: "status_only", Enum: []string{"status_only", "milestones"},
			Group: GroupTools, Title: "Work narration",
			Help: "status_only stays quiet until you ask what's happening; milestones speaks a short line each time the worker moves from one kind of work to another.",
		},
		{
			Key: "voice_failed_route_repair", Type: TypeBool, Default: true,
			Group: GroupTools, Title: "Failed route repair",
			Help: "When the model speaks a tool promise without calling a tool, classify the turn as spawn instead of reading the promise aloud.",
		},
		{
			Key: "tool_command_timeout", Type: TypeInt, Default: 30, Unit: "seconds", Min: num(1), Max: num(120),
			Group: GroupTools, Title: "Command timeout",
			Help: "How long one command may run before it is killed. Long builds need a bigger number.",
		},
		{
			Key: "skills_enabled", Type: TypeBool, Default: true,
			Group: GroupTools, Title: "Skills",
			Help: "Loads SKILL.md instruction packs into her prompt. Ollama only.",
		},
		{
			Key: "skills_dir", Type: TypeString, Default: "",
			Group: GroupTools, Title: "Skills folder",
			Help: "An extra folder scanned for skills. Empty uses the folder inside Obey Voice's install root.",
		},
		{
			Key: "work_dir", Type: TypeString, Default: "",
			Group: GroupTools, Title: "Working folder",
			Help: "The folder the agent's file and shell tools start in — a default, not a sandbox. Empty uses the folder it was launched from, or your home folder when that is the binary's own folder.",
		},
		{
			Key: "skills_disabled", Type: TypeStringList, Default: []string{},
			Group: GroupTools, Title: "Disabled skills",
			Help: "Skills the agent ignores even when discovered.",
		},
		{
			Key: "ollama_embedding_model", Type: TypeString, Default: "nomic-embed-text",
			Group: GroupTools, Title: "Skill matching model",
			Help: "The embedding model that decides which skill fits a request. Empty falls back to name matching only.",
		},
		{
			Key: "skills_similarity_threshold", Type: TypeFloat, Default: 0.55, Min: num(0.0), Max: num(1.0),
			Group: GroupTools, Title: "Skill match cutoff",
			Help: "How close a request must be to a skill before it is offered. Higher offers fewer, more relevant skills.",
		},
	}...)
}

func remoteKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "remote_tools_enabled", Type: TypeBool, Default: false,
			Group: GroupRemote, Title: "Tools for network turns",
			Help: "Lets turns that arrive over the network run tools on this Mac. Off by default — a phone or browser can otherwise run commands here.",
		},
	}
}
