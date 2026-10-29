//! Presentation follows obey-voice/internal/tui/settings_rows*.go and settings_view.go.
//! The engine schema remains the authority for values, validation, and persistence.
use crate::model::KeySpec;

pub(super) const SECTIONS: &[(&str, &str)] = &[
    ("brain", "Brain"),
    ("workers", "Workers"),
    ("tools", "Tools"),
    ("voice", "Voice"),
    ("qwen", "Qwen"),
    ("stt", "STT"),
    ("speakers", "Speakers"),
    ("audio", "Audio"),
    ("meeting", "Meeting"),
    ("remote", "Remote"),
    ("models", "Models"),
    ("advanced", "Advanced"),
];

pub(super) fn title(section: &str) -> &str {
    SECTIONS
        .iter()
        .find(|(id, _)| *id == section)
        .map_or(section, |(_, title)| title)
}

pub(super) fn section(spec: &KeySpec) -> &'static str {
    let key = spec.key.as_str();
    if key.starts_with("claude_") || key.starts_with("continuity_") {
        return "advanced";
    }
    if key.starts_with("voice_worker") {
        return "workers";
    }
    if matches!(key, "input_device" | "output_device" | "playback_volume") {
        return "audio";
    }
    if matches!(
        key,
        "brain_provider"
            | "ollama_model"
            | "grok_model"
            | "cursor_model"
            | "obey_provider"
            | "obey_model"
            | "obey_camp"
            | "obey_socket"
            | "obey_tool_policy"
            | "codex_model"
            | "codex_reasoning_effort"
    ) || key.starts_with("decision_")
    {
        return "brain";
    }
    if matches!(key, "qwen_tts_voice" | "qwen_tts_language") {
        return "voice";
    }
    if key.starts_with("qwen_")
        || key.starts_with("tts_worker_")
        || key.starts_with("tts_lan_")
        || key == "tts_place"
    {
        return "qwen";
    }
    if [
        "stt_",
        "whisper",
        "parakeet_",
        "sherpa_",
        "transcribecpp_",
        "vad_",
    ]
    .iter()
    .any(|prefix| key.starts_with(prefix))
        || matches!(key, "language" | "listen_timeout" | "phrase_time_limit")
    {
        return "stt";
    }
    if key.starts_with("speaker.meeting.") || spec.group == "meetings" {
        return "meeting";
    }
    if key.starts_with("speaker.") || spec.group == "speakers" {
        return "speakers";
    }
    if ["barge_in_", "name_summon_", "talk_hotkey", "voice_frontend"]
        .iter()
        .any(|prefix| key.starts_with(prefix))
    {
        return "tools";
    }
    match spec.group.as_str() {
        "voice" => "voice",
        "tools" => "tools",
        "remote" => "remote",
        "models" => "models",
        // Unrecognized and low-level schema entries stay reachable here and in search.
        _ => "advanced",
    }
}

fn provider_model(key: &str) -> Option<&'static str> {
    match key {
        "ollama_model" => Some("ollama"),
        "grok_model" => Some("grok"),
        "cursor_model" => Some("cursor"),
        "codex_model" | "codex_reasoning_effort" => Some("codex"),
        "obey_provider" | "obey_model" | "obey_camp" | "obey_socket" | "obey_tool_policy" => {
            Some("obey")
        }
        _ => None,
    }
}

fn priority(key: &str) -> usize {
    if let Some(index) = [
        "decision_enabled",
        "decision_voice_work",
        "decision_text_work",
        "decision_provider",
        "decision_runtime",
        "decision_local_model",
        "decision_endpoint",
        "decision_model",
        "decision_budget_ms",
        "decision_policy",
    ]
    .iter()
    .position(|candidate| *candidate == key)
    {
        return 2 + index;
    }
    // Put primary choices first, as the TUI does. Remaining rows keep schema order.
    match key {
        "brain_provider"
        | "voice_worker_provider"
        | "tts_provider"
        | "stt_provider"
        | "input_device"
        | "tts_place"
        | "user_display_name" => 0,
        "ollama_model"
        | "grok_model"
        | "cursor_model"
        | "obey_provider"
        | "obey_model"
        | "obey_camp"
        | "obey_socket"
        | "obey_tool_policy"
        | "codex_model"
        | "voice_worker_model"
        | "tts_voice"
        | "qwen_tts_voice"
        | "audio8_voice"
        | "openai_speech_voice"
        | "output_device" => 1,
        "codex_reasoning_effort" | "qwen_tts_language" | "playback_volume" => 2,
        _ => 100,
    }
}

pub(super) fn visible_keys(
    schema: &[KeySpec],
    selected: &str,
    search: &str,
    provider: &str,
) -> Vec<KeySpec> {
    let query = search.trim().to_lowercase();
    let mut keys: Vec<_> = schema
        .iter()
        .filter(|spec| {
            if spec.group == "hidden" {
                return false;
            }
            if !query.is_empty() {
                return format!(
                    "{} {} {} {}",
                    spec.key,
                    spec.title,
                    spec.help,
                    title(section(spec))
                )
                .to_lowercase()
                .contains(&query);
            }
            section(spec) == selected
                && (selected != "brain" || provider_model(&spec.key).is_none_or(|p| p == provider))
        })
        .cloned()
        .collect();
    keys.sort_by_key(|spec| priority(&spec.key));
    keys
}

#[cfg(test)]
mod tests {
    use super::*;

    fn spec(key: &str, group: &str) -> KeySpec {
        KeySpec {
            key: key.into(),
            group: group.into(),
            ..Default::default()
        }
    }

    #[test]
    fn schema_groups_are_mapped_to_tui_workflows() {
        for (key, group, expected) in [
            ("voice_worker_model", "brain", "workers"),
            ("input_device", "voice", "audio"),
            ("playback_volume", "voice", "audio"),
            ("qwen_tts_model_tier", "models", "qwen"),
            ("tts_worker_url", "voice", "qwen"),
            ("qwen_tts_language", "models", "voice"),
            ("stt_provider", "models", "stt"),
            ("speaker.meeting.enabled", "meetings", "meeting"),
            ("talk_hotkey", "voice", "tools"),
            ("claude_max_session_tokens", "brain", "advanced"),
            ("claude_session_warn_tokens", "brain", "advanced"),
            ("future_setting", "new_group", "advanced"),
        ] {
            assert_eq!(section(&spec(key, group)), expected, "{key}");
        }
    }

    #[test]
    fn brain_shows_selected_provider_while_search_keeps_other_settings_reachable() {
        let schema = vec![
            spec("claude_max_session_tokens", "brain"),
            spec("grok_model", "brain"),
            spec("codex_model", "brain"),
            spec("codex_reasoning_effort", "brain"),
            spec("ollama_model", "brain"),
            spec("brain_provider", "brain"),
            spec("decision_api_key", "hidden"),
        ];
        let brain = visible_keys(&schema, "brain", "", "ollama");
        assert_eq!(
            brain.iter().map(|s| s.key.as_str()).collect::<Vec<_>>(),
            ["brain_provider", "ollama_model"]
        );
        assert_eq!(
            visible_keys(&schema, "brain", "claude", "ollama")[0].key,
            "claude_max_session_tokens"
        );
        assert_eq!(
            visible_keys(&schema, "brain", "grok", "ollama")[0].key,
            "grok_model"
        );
        assert!(visible_keys(&schema, "brain", "decision_api", "ollama").is_empty());
        assert_eq!(
            visible_keys(&schema, "brain", "", "codex")
                .iter()
                .map(|s| s.key.as_str())
                .collect::<Vec<_>>(),
            ["brain_provider", "codex_model", "codex_reasoning_effort"]
        );
    }
}
