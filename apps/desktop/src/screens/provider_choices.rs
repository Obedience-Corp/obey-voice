use crate::model::KeySpec;
use serde_json::Value;

pub(super) fn saved_voice(values: &Value) -> String {
    let provider = values["tts_provider"]["value"].as_str().unwrap_or("kokoro");
    let key = match provider {
        "kokoro" => "tts_voice",
        "qwen3-tts" => "qwen_tts_voice",
        "audio8" => "audio8_voice",
        "openai-speech" => "openai_speech_voice",
        // Let the engine resolve an unknown provider's default voice.
        _ => return String::new(),
    };
    values[key]["value"].as_str().unwrap_or_default().into()
}

// Preserve inherited and saved values while schema discovery is pending or
// fails. The engine remains responsible for validating a saved provider.
pub(super) fn persona_choices(schema: &[KeySpec], key: &str, saved: &str) -> Vec<String> {
    let mut choices = vec![String::new()];
    if let Some(spec) = schema.iter().find(|spec| spec.key == key) {
        for choice in &spec.choices {
            if !choices.contains(choice) {
                choices.push(choice.clone());
            }
        }
    }
    if !choices.iter().any(|choice| choice == saved) {
        choices.push(saved.into());
    }
    choices
}

pub(super) fn speech_fields(provider: &str) -> &'static [&'static str] {
    match provider {
        "openai-speech" => &[
            "openai_speech_url",
            "openai_speech_model",
            "openai_speech_voice",
            "openai_speech_api_key",
            "openai_speech_timeout",
        ],
        "qwen3-tts" => &["qwen_tts_voice"],
        "audio8" => &["audio8_voice"],
        "kokoro" => &["tts_voice"],
        _ => &[],
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn preview_uses_the_selected_provider_voice() {
        let mut values = serde_json::json!({
            "tts_provider": {"value": "openai-speech"},
            "tts_voice": {"value": "af_heart"},
            "openai_speech_voice": {"value": "server-voice"},
            "voice": {"value": "wrong-legacy-value"}
        });
        assert_eq!(saved_voice(&values), "server-voice");
        values["tts_provider"]["value"] = serde_json::json!("kokoro");
        assert_eq!(saved_voice(&values), "af_heart");
        values["tts_provider"]["value"] = serde_json::json!("future-provider");
        assert_eq!(saved_voice(&values), "");
    }

    #[test]
    fn engine_choices_reach_personas_without_losing_inheritance_or_saved_values() {
        let schema = vec![KeySpec {
            key: "brain_provider".into(),
            choices: vec!["ollama".into(), "codex".into(), "future-provider".into()],
            ..Default::default()
        }];
        assert_eq!(
            persona_choices(&schema, "brain_provider", "retired-provider"),
            ["", "ollama", "codex", "future-provider", "retired-provider"]
        );
        assert_eq!(
            persona_choices(&[], "tts_provider", "audio8"),
            ["", "audio8"]
        );
    }

    #[test]
    fn speech_setup_uses_provider_specific_keys() {
        let fields = speech_fields("openai-speech");
        assert!(fields.contains(&"openai_speech_voice"));
        assert!(fields.contains(&"openai_speech_url"));
        assert!(!fields.contains(&"tts_voice"));
        assert!(speech_fields("unknown-provider").is_empty());
    }
}
