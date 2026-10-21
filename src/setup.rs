//! First-run progress and readiness. Completion is not a permanent health claim.
use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct Progress {
    pub version: u8,
    pub step: usize,
    pub completed: bool,
    pub deferred: bool,
    pub text_only: bool,
    pub silent: bool,
}
impl Default for Progress {
    fn default() -> Self {
        Self {
            version: 1,
            step: 0,
            completed: false,
            deferred: false,
            text_only: false,
            silent: false,
        }
    }
}
#[derive(Clone, Debug)]
pub enum Operation {
    Load,
    Save(Progress),
    Provider,
    Models,
    Install,
    Start,
    Stop,
    Finish,
}
#[derive(Default)]
pub struct Setup {
    pub progress: Progress,
    pub loaded: bool,
    pub existing: bool,
    pub config_path: String,
    pub warning: String,
    pub error: Option<String>,
    pub busy: Option<u64>,
    pub provider: Option<Value>,
    pub models: Option<Vec<Value>>,
    pub download: Value,
    pub download_cancelled: bool,
    pub mic_confirmed: bool,
    pub speaker_confirmed: bool,
    pub sent_test: bool,
    pub reply_baseline: usize,
    pub reply_received: bool,
}
impl Setup {
    pub fn invalidate(&mut self) {
        self.provider = None;
        self.models = None;
        self.mic_confirmed = false;
        self.speaker_confirmed = false;
        self.sent_test = false;
        self.reply_received = false;
    }
    pub fn provider_ready(&self) -> bool {
        self.provider
            .as_ref()
            .is_some_and(|v| v["severity"] == "ok")
    }
    pub fn models_ready(&self) -> bool {
        self.models.as_ref().is_some_and(|items| {
            items.iter().all(|v| {
                // The engine's native-package row covers the selected Qwen tier.
                // Per-tier rows also advertise optional tiers; do not require those.
                v["id"]
                    .as_str()
                    .unwrap_or_default()
                    .starts_with("tts.qwen3.tier.")
                    || v["installed"] == true
            })
        })
    }
    pub fn can_finish(&self) -> bool {
        self.reply_received
            && (self.progress.text_only || self.mic_confirmed)
            && (self.progress.silent || self.speaker_confirmed)
    }
    pub fn model_args(&self, action: &str) -> Option<Vec<String>> {
        if self.progress.text_only && self.progress.silent {
            return None;
        }
        let mut args = vec!["models".into(), action.into(), "--json".into()];
        if !self.progress.text_only {
            args.extend(["--stt".into(), "--vad".into(), "--speaker".into()]);
        }
        if !self.progress.silent {
            args.push("--tts".into());
        }
        Some(args)
    }
}

/// CLI failures may contain NDJSON progress followed by a terminal error.
pub fn error_message(raw: &str) -> String {
    let message = |v: &Value| {
        v["error"]
            .as_str()
            .or_else(|| v["error"]["message"].as_str())
            .map(str::to_owned)
    };
    if let Ok(v) = serde_json::from_str::<Value>(raw)
        && let Some(text) = message(&v)
    {
        return text;
    }
    for line in raw.lines().rev() {
        if let Ok(v) = serde_json::from_str::<Value>(line)
            && let Some(text) = message(&v)
        {
            return text;
        }
    }
    let text: String = raw.chars().take(2000).collect();
    if text.trim().is_empty() {
        "The operation stopped. Open Diagnostics for details, then retry.".into()
    } else {
        text
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn download_errors_show_the_remedy_not_progress_json() {
        assert_eq!(
            error_message(
                "{\"phase\":\"download\"}\n{\"done\":true,\"error\":\"Retry connection\"}"
            ),
            "Retry connection"
        );
        assert_eq!(
            error_message(r#"{"error":{"message":"Choose a valid model"}}"#),
            "Choose a valid model"
        );
    }
    #[test]
    fn text_only_never_requests_all_assets() {
        let mut s = Setup::default();
        s.progress.text_only = true;
        s.progress.silent = true;
        assert!(s.model_args("ensure").is_none());
        s.progress.silent = false;
        assert_eq!(
            s.model_args("status").unwrap(),
            ["models", "status", "--json", "--tts"]
        );
    }
    #[test]
    fn completion_requires_reply_and_enabled_audio_confirmation() {
        let mut s = Setup::default();
        assert!(!s.can_finish());
        s.reply_received = true;
        assert!(!s.can_finish());
        s.mic_confirmed = true;
        assert!(!s.can_finish());
        s.speaker_confirmed = true;
        assert!(s.can_finish());
        s.invalidate();
        assert!(!s.can_finish());
    }
    #[test]
    fn optional_qwen_tiers_do_not_block_selected_package() {
        let mut s = Setup {
            models: Some(vec![
                json!({"id":"tts.qwen3.native","installed":true}),
                json!({"id":"tts.qwen3.tier.1.7b","installed":false}),
            ]),
            ..Default::default()
        };
        assert!(s.models_ready());
        s.models
            .as_mut()
            .unwrap()
            .push(json!({"id":"stt.whisper","installed":false}));
        assert!(!s.models_ready());
    }
}
