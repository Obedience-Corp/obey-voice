use crate::app::VoiceApp;
use crate::model::{Purpose, display};
use crate::screens::card;
use crate::setup::Operation;
use eframe::egui;
use serde_json::json;

impl VoiceApp {
    pub(super) fn setup_brain(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.heading("Choose how Obey thinks");
            ui.label("Ollama runs models on your computer or your own server. Other providers use their installed CLI and account.");
            ui.label("For a CLI provider, install it and sign in through its terminal command first. The test conversation checks that your account can reply.");
            if !self.settings_persona.is_null() {
                ui.label("Your active persona can override these defaults. The check and test use that persona, just like a normal conversation.");
            }
            if ui.button("Check provider").clicked() {
                self.setup.provider = None;
                self.setup_request("setup_provider", json!({}), Operation::Provider);
            }
            if let Some(check) = &self.setup.provider {
                ui.strong(if check["severity"] == "ok" {
                    "Provider requirements checked"
                } else {
                    "Provider needs attention"
                });
                ui.label(display(&check["detail"]));
                if let Some(remedy) = check["remediation"].as_str() {
                    ui.label(remedy);
                }
            }
        });
        self.setup_fields(ui, &["brain_provider"]);
        let provider = self.values["brain_provider"]["value"]
            .as_str()
            .unwrap_or("ollama");
        let fields: &[&str] = match provider {
            "ollama" => &["ollama_host", "ollama_model"],
            "grok" => &["grok_model"],
            "cursor" => &["cursor_model"],
            "codex" => &["codex_model", "codex_reasoning_effort"],
            _ => &[],
        };
        self.setup_fields(ui, fields);
        if let Some(models) = self
            .setup
            .provider
            .as_ref()
            .and_then(|p| p["models"].as_array())
            .cloned()
        {
            ui.collapsing("Models installed on this server", |ui| {
                for model in models.iter().filter_map(|v| v.as_str()) {
                    if ui.button(format!("Use {model}")).clicked() {
                        self.cli(
                            vec![
                                "config".into(),
                                "set".into(),
                                "ollama_model".into(),
                                model.into(),
                                "--json".into(),
                            ],
                            Purpose::SettingSave("ollama_model".into()),
                            false,
                        );
                    }
                }
            });
        }
    }
    pub(super) fn setup_speech(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.heading("Make it a voice conversation—or use text");
            let mut mic = !self.setup.progress.text_only;
            let mut speaker = !self.setup.progress.silent;
            let changed = ui.checkbox(&mut mic, "Microphone input").changed()
                | ui.checkbox(&mut speaker, "Spoken replies").changed();
            if changed {
                self.setup.progress.text_only = !mic;
                self.setup.progress.silent = !speaker;
                self.setup.invalidate();
                self.setup_save(2, false, false);
            }
            ui.label("Turn both off for text-only setup. No microphone is opened on this page.");
            ui.add_space(8.);
            if ui.button("Check speech models").clicked() {
                self.setup_models(false);
            }
            if let Some(models) = &self.setup.models {
                if models.is_empty() {
                    ui.label("No local speech models needed for this selection.");
                }
                for model in models {
                    let optional = model["id"]
                        .as_str()
                        .unwrap_or_default()
                        .starts_with("tts.qwen3.tier.");
                    if optional {
                        continue;
                    }
                    ui.label(format!(
                        "{} · {}",
                        display(&model["name"]),
                        if model["installed"] == true {
                            "Installed"
                        } else {
                            "Needs installation"
                        }
                    ));
                    if let Some(detail) = model["detail"].as_str() {
                        ui.small(detail);
                    }
                }
                if !self.setup.models_ready() {
                    ui.label("Downloads may be large. We’ll show sizes and progress as the server reports them. Cancel or retry at any time.");
                    if ui.button("Install missing speech models").clicked() {
                        self.setup_models(true);
                    }
                }
            }
        });
        if !self.setup.progress.text_only {
            self.setup_fields(ui, &["stt_provider", "stt_mode"]);
        }
        if !self.setup.progress.silent {
            self.setup_fields(ui, &["tts_provider"]);
            let provider = self.values["tts_provider"]["value"]
                .as_str()
                .unwrap_or("kokoro");
            let voice = match provider {
                "qwen3-tts" => "qwen_tts_voice",
                "audio8" => "audio8_voice",
                _ => "tts_voice",
            };
            self.setup_fields(ui, &[voice]);
        }
    }
    pub(super) fn setup_fields(&mut self, ui: &mut egui::Ui, fields: &[&str]) {
        for key in fields {
            if let Some(spec) = self.schema.iter().find(|s| s.key == *key).cloned() {
                self.setup_setting_editor(ui, &spec);
            }
        }
    }
}
