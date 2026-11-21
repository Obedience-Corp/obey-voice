//! Speech model setup delegates downloads and verification to the engine.
use crate::app::VoiceApp;
use crate::model::Purpose;
use crate::screens::card;
use eframe::egui;
use serde_json::Value;

pub(crate) fn is_model_key(key: &str) -> bool {
    matches!(
        key,
        "parakeet_model"
            | "whisper_model"
            | "sherpa_streaming_model"
            | "whispercpp_model"
            | "transcribecpp_model"
    )
}

pub(crate) fn is_selection(key: &str) -> bool {
    is_model_key(key) || matches!(key, "stt_provider" | "stt_mode")
}

pub(crate) fn installs_selection(key: &str, values: &Value) -> bool {
    matches!(key, "stt_provider" | "stt_mode") || key == selected_model_key(values)
}

pub(super) fn selected_model_key(values: &Value) -> &'static str {
    let provider = values["stt_provider"]["value"].as_str().unwrap_or("");
    match provider {
        "sherpa" | "sherpa-offline" | "sherpa-streaming" => {
            if provider == "sherpa-streaming" || values["stt_mode"]["value"] == "streaming" {
                "sherpa_streaming_model"
            } else {
                "whisper_model"
            }
        }
        "whispercpp" => "whispercpp_model",
        "transcribecpp" => "transcribecpp_model",
        "" | "parakeet" | "sherpa-parakeet" => "parakeet_model",
        _ => "",
    }
}

pub(crate) fn arguments(install: bool) -> Vec<String> {
    [
        "models",
        if install { "ensure" } else { "status" },
        "--stt",
        "--vad",
        "--json",
    ]
    .map(str::to_owned)
    .to_vec()
}

impl VoiceApp {
    pub(crate) fn stt_setup_busy(&self) -> bool {
        self.task.is_some()
            || self.pending.values().any(|p| {
                matches!(
                    p,
                    Purpose::STTProviderSave | Purpose::STTModelSave | Purpose::SettingSave(_)
                )
            })
    }

    pub(crate) fn start_stt_install(&mut self) {
        self.error = None;
        self.setup.download = Value::Null;
        self.start_task("Install selected speech model", arguments(true));
        self.status = "Installing selected speech model; applies to a new conversation".into();
    }

    pub(super) fn stt_model_setup(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.heading("Speech recognition models");
            ui.label("Choose a recognizer and its model below. Save and install downloads the selected model and verifies existing files. Changes apply to a new conversation.");
            ui.label("Parakeet v2 is English-only. Choose Multilingual (25 languages) — Parakeet v3 for supported European languages.");
            ui.horizontal_wrapped(|ui| {
                ui.add_enabled_ui(!self.stt_setup_busy(), |ui| {
                    if ui.button("Check selected model").clicked() {
                        self.start_task("Speech model status", arguments(false));
                    }
                    if ui.button("Install / repair selected model").clicked() {
                        self.start_stt_install();
                    }
                });
            });
            ui.label("After a failed or canceled download, use Install / repair to retry. Models folder is configured under Models. Whisper.cpp also needs its separately installed executable.");
            if self.task_title == "Install selected speech model" && self.task.is_some() {
                let p = &self.setup.download;
                ui.label(format!(
                    "{} · {} · {:.0}%",
                    p["asset"].as_str().unwrap_or("Selected model"),
                    p["phase"].as_str().unwrap_or("Preparing"),
                    p["pct"].as_f64().unwrap_or(0.)
                ));
                if let Some(id) = self.task
                    && ui.button("Cancel model installation").clicked()
                {
                    self.backend.cancel(id);
                    self.status = "Cancel requested…".into();
                }
            }
        });
    }
}
