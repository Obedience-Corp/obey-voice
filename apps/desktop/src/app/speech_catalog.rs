use super::VoiceApp;
use crate::model::Purpose;
use eframe::egui;
use serde_json::{Value, json};

#[derive(Default)]
pub(crate) struct SpeechCatalog {
    pending: Option<u64>,
    result: Value,
}

pub(super) fn affects_speech_catalog(key: &str) -> bool {
    key.starts_with("openai_speech_") && key != "openai_speech_voice"
}

impl SpeechCatalog {
    pub(super) fn complete(&mut self, id: u64, result: Result<Value, String>) {
        if self.pending == Some(id) {
            self.pending = None;
            self.result = result.unwrap_or_else(|error| json!({"notice": error}));
        }
    }

    pub(crate) fn picker(&self, ui: &mut egui::Ui, voice: &mut String, live: bool) -> bool {
        let loading = self.pending.is_some();
        let refresh = ui
            .add_enabled(
                live && !loading,
                egui::Button::new(if loading {
                    "Loading voices…"
                } else if self.result.is_null() {
                    "Browse available voices"
                } else {
                    "Refresh voices"
                }),
            )
            .clicked();
        if let Some(notice) = self.result["notice"].as_str() {
            ui.small(notice);
        }
        if let Some(voices) = self.result["voices"].as_array() {
            egui::ComboBox::from_id_salt("available_speech_voices")
                .selected_text("Choose a voice")
                .show_ui(ui, |ui| {
                    for entry in voices {
                        if let Some(name) = entry["name"].as_str() {
                            let locale = entry["locale"].as_str().unwrap_or_default();
                            let label = if locale.is_empty() {
                                name.into()
                            } else {
                                format!("{name} · {locale}")
                            };
                            ui.selectable_value(voice, name.into(), label)
                                .on_hover_text(entry["description"].as_str().unwrap_or_default());
                        }
                    }
                });
        }
        refresh
    }
}

impl VoiceApp {
    pub(crate) fn load_speech_voices(&mut self) {
        let id = self.cli(
            ["voices", "--provider", "openai-speech", "--json"]
                .map(str::to_owned)
                .to_vec(),
            Purpose::SpeechVoices,
            false,
        );
        self.speech_catalog.pending = Some(id);
    }
}
