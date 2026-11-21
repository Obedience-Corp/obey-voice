use super::stt;
use crate::app::VoiceApp;
use crate::model::{KeySpec, Purpose, display, string};
use crate::screens::card;
use crate::theme::muted;
use eframe::egui::{self, RichText};

impl VoiceApp {
    pub(crate) fn save_setting_choice(&mut self, key: &str, value: Option<String>) {
        if let Some(reason) = stt::selection_override_reason(key, &self.values) {
            self.error = Some(reason);
            return;
        }
        let mut args = vec![
            "config".into(),
            if value.is_some() { "set" } else { "unset" }.into(),
            key.into(),
        ];
        if let Some(value) = value {
            args.push(value);
        }
        args.push("--json".into());
        let purpose = match key {
            "stt_provider" => Purpose::STTProviderSave,
            "whispercpp_model" => Purpose::STTModelSave,
            _ => Purpose::SettingSave(key.into()),
        };
        self.cli(args, purpose, false);
    }

    pub(crate) fn setup_setting_editor(&mut self, ui: &mut egui::Ui, spec: &KeySpec) {
        let stt_selection = stt::is_selection(&spec.key);
        let installs_selection = stt::installs_selection(&spec.key, &self.values);
        let override_reason = stt::selection_override_reason(&spec.key, &self.values);
        let enabled = !stt_selection || (!self.stt_setup_busy() && override_reason.is_none());
        if let Some(reason) = &override_reason {
            ui.label(reason);
        }
        ui.add_enabled_ui(enabled, |ui| {
            ui.push_id(&spec.key, |ui| {
                card(ui, |ui| {
                    ui.spacing_mut().item_spacing.y = 8.;
                    ui.strong(&spec.title).on_hover_text(&spec.help);
                    let current = self.values[&spec.key]["value"].clone();
                    let saved = display(if current.is_null() {
                        &spec.default
                    } else {
                        &current
                    });
                    if !spec.editable {
                        ui.label(format!("Managed by {}", spec.managed_by));
                        return;
                    }
                    if self.values[&spec.key]["source"] == "env" {
                        ui.small("An environment variable overrides this saved setting.");
                    }
                    let value = self.drafts.entry(spec.key.clone()).or_insert(saved.clone());
                    setting_input(ui, spec, value);
                    let load = spec.key == "openai_speech_voice"
                        && self.speech_catalog.picker(ui, value, !self.demo);
                    let draft = value.clone();
                    if load {
                        self.load_speech_voices();
                    }
                    if ui
                        .add_enabled(
                            draft != saved,
                            egui::Button::new(if installs_selection {
                                "Save and install"
                            } else {
                                "Save choice"
                            }),
                        )
                        .clicked()
                    {
                        self.save_setting_choice(&spec.key, Some(draft));
                    }
                });
            });
        });
    }
    pub(crate) fn setting_editor(&mut self, ui: &mut egui::Ui, spec: &KeySpec) {
        let stt_selection = stt::is_selection(&spec.key);
        let installs_selection = stt::installs_selection(&spec.key, &self.values);
        let override_reason = stt::selection_override_reason(&spec.key, &self.values);
        let enabled = !stt_selection || (!self.stt_setup_busy() && override_reason.is_none());
        if let Some(reason) = &override_reason {
            ui.label(reason);
        }
        ui.add_enabled_ui(enabled, |ui| {
            ui.push_id(&spec.key, |ui| {
                card(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.strong(&spec.title);
                        if spec.restart_required {
                            ui.label(RichText::new("Next conversation").small().color(muted(ui)));
                        }
                    });
                    ui.label(RichText::new(&spec.help).small().color(muted(ui)));
                    let current = self.values[&spec.key]["value"].clone();
                    let source = string(&self.values[&spec.key], "source");
                    ui.label(
                        RichText::new(format!("{} · {}", spec.key, source))
                            .monospace()
                            .small()
                            .color(muted(ui)),
                    );
                    if !spec.editable {
                        ui.label(format!("Managed by {}", spec.managed_by));
                        return;
                    }
                    let value = self.drafts.entry(spec.key.clone()).or_insert_with(|| {
                        display(if current.is_null() {
                            &spec.default
                        } else {
                            &current
                        })
                    });
                    setting_input(ui, spec, value);
                    let saved = display(if current.is_null() {
                        &spec.default
                    } else {
                        &current
                    });
                    let load = spec.key == "openai_speech_voice"
                        && self.speech_catalog.picker(ui, value, !self.demo);
                    let draft = value.clone();
                    let changed = draft != saved;
                    if load {
                        self.load_speech_voices();
                    }
                    ui.horizontal(|ui| {
                        if ui
                            .add_enabled(
                                changed,
                                egui::Button::new(if installs_selection {
                                    "Save and install"
                                } else {
                                    "Save"
                                }),
                            )
                            .clicked()
                        {
                            self.save_setting_choice(&spec.key, Some(draft));
                        }
                        if ui.button("Reset to default").clicked() {
                            self.save_setting_choice(&spec.key, None);
                        }
                    });
                });
            });
        });
    }
}

fn setting_input(ui: &mut egui::Ui, spec: &KeySpec, value: &mut String) {
    match spec.kind.as_str() {
        "bool" => {
            let mut on = value == "true";
            if ui.checkbox(&mut on, "Enabled").changed() {
                *value = on.to_string();
            }
        }
        "enum" => {
            egui::ComboBox::from_id_salt("value")
                .selected_text(if value.is_empty() {
                    "App default"
                } else {
                    spec.enum_labels
                        .get(value)
                        .map(String::as_str)
                        .unwrap_or(value.as_str())
                })
                .show_ui(ui, |ui| {
                    if spec.allows_empty {
                        ui.selectable_value(value, String::new(), "App default");
                    }
                    for choice in &spec.choices {
                        ui.selectable_value(
                            value,
                            choice.clone(),
                            spec.enum_labels.get(choice).unwrap_or(choice),
                        );
                    }
                });
        }
        _ => {
            if !spec.suggestions.is_empty() {
                egui::ComboBox::from_id_salt("suggested_model")
                    .selected_text("Choose an available model")
                    .show_ui(ui, |ui| {
                        for choice in &spec.suggestions {
                            ui.selectable_value(value, choice.clone(), choice);
                        }
                    });
            }
            ui.add(
                egui::TextEdit::singleline(value)
                    .password(spec.secret)
                    .desired_width(f32::INFINITY),
            );
        }
    }
}
