use super::{card, field};
use crate::app::VoiceApp;
use crate::model::{Purpose, display, string};
use crate::theme::muted;
use eframe::egui;
use eframe::egui::RichText;

impl VoiceApp {
    pub(super) fn settings(&mut self, ui: &mut egui::Ui) {
        ui.horizontal_wrapped(|ui| {
            for group in [
                "voice", "brain", "speakers", "meetings", "tools", "remote", "advanced", "models",
            ] {
                ui.selectable_value(&mut self.group, group.into(), group);
            }
            let mut extra: Vec<_> = self
                .schema
                .iter()
                .map(|s| s.group.as_str())
                .filter(|g| {
                    ![
                        "hidden", "voice", "brain", "speakers", "meetings", "tools", "remote",
                        "advanced", "models",
                    ]
                    .contains(g)
                })
                .collect();
            extra.sort_unstable();
            extra.dedup();
            for group in extra {
                ui.selectable_value(&mut self.group, group.into(), group);
            }
        });
        ui.horizontal(|ui| {
            ui.add(egui::TextEdit::singleline(&mut self.search).hint_text("Find a setting…"));
            if ui.button("Reload").clicked() {
                self.command(&["config", "get", "--json"], Purpose::SettingsValues);
            }
        });
        if !self.settings_persona.is_null() {
            ui.collapsing("Active persona overrides", |ui| {
                ui.label(
                    "Persona model and voice choices can override these application defaults.",
                );
                ui.monospace(
                    serde_json::to_string_pretty(&self.settings_persona).unwrap_or_default(),
                );
            });
        }
        if self.group == "models" {
            ui.horizontal(|ui| {
                if ui.button("Check models").clicked() {
                    self.start_task(
                        "Model status",
                        vec!["models".into(), "status".into(), "--json".into()],
                    );
                }
                if ui.button("Install required models").clicked() {
                    self.start_task(
                        "Install models",
                        vec!["models".into(), "ensure".into(), "--json".into()],
                    );
                }
                if ui.button("Run diagnostics").clicked() {
                    self.start_task("Diagnostics", vec!["doctor".into()]);
                }
            });
        }
        let keys: Vec<_> = self
            .schema
            .iter()
            .filter(|s| {
                s.group != "hidden"
                    && if self.search.is_empty() {
                        s.group == self.group
                    } else {
                        format!("{} {} {}", s.key, s.title, s.help)
                            .to_lowercase()
                            .contains(&self.search.to_lowercase())
                    }
            })
            .cloned()
            .collect();
        egui::ScrollArea::vertical()
            .id_salt("settings_scroll")
            .show(ui, |ui| {
                if self.group == "speakers" && self.search.is_empty() {
                    card(ui, |ui| {
                        ui.strong("Voice registration");
                        if !self.active { ui.label("Open a voice conversation, then return here to register a voice using its microphone."); }
                        field(ui, "Speaker name", &mut self.enroll_name);
                        ui.checkbox(&mut self.enroll_owner, "Register as your voice (owner)");
                        if self.state.enrollment.active {
                            let e = &self.state.enrollment;
                            ui.label(if e.processing { format!("Learning {}’s voice…", e.name) } else { format!("Recording {} · clip {} of 3 · {:.1} seconds", e.name, e.clip, e.seconds) });
                            if ui.button("Cancel registration").clicked() { self.action("enroll_cancel", "", false); }
                        } else if ui.add_enabled(self.active && self.state.voice_available && !self.enroll_name.trim().is_empty(), egui::Button::new("Record voice samples")).clicked() {
                            self.action("enroll", &self.enroll_name.clone(), self.enroll_owner);
                        }
                        for name in self.state.speaker_names.clone() { ui.horizontal(|ui| {
                            ui.label(&name);
                            if ui.small_button("Remove registration…").clicked() { self.confirm = Some((format!("Remove the voice registration for {name}?"), vec!["@enroll_remove".into(),name])); }
                        }); }
                        ui.label(&self.state.settings_message);
                    });
                }
                if self.group == "voice" && self.active && self.search.is_empty() {
                    ui.horizontal(|ui| {
                        if ui.button("Preview saved voice").clicked() {
                            let voice = self.values["voice"]["value"].as_str().unwrap_or_default().to_string();
                            self.action("preview", &voice, false);
                        }
                        if ui.button("Stop preview").clicked() { self.action("preview_stop", "", false); }
                        ui.label(&self.state.settings_message);
                    });
                }
                for spec in keys {
                    ui.push_id(&spec.key, |ui| {
                        card(ui, |ui| {
                            ui.horizontal(|ui| {
                                ui.strong(&spec.title);
                                if spec.restart_required {
                                    ui.label(
                                        RichText::new("Next conversation").small().color(muted(ui)),
                                    );
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
                                            value.as_str()
                                        })
                                        .show_ui(ui, |ui| {
                                            if spec.allows_empty {
                                                ui.selectable_value(
                                                    value,
                                                    String::new(),
                                                    "App default",
                                                );
                                            }
                                            for choice in &spec.choices {
                                                ui.selectable_value(value, choice.clone(), choice);
                                            }
                                        });
                                }
                                _ => {
                                    ui.add(
                                        egui::TextEdit::singleline(value)
                                            .password(spec.secret)
                                            .desired_width(f32::INFINITY),
                                    );
                                }
                            }
                            let saved = display(if current.is_null() {
                                &spec.default
                            } else {
                                &current
                            });
                            let changed = *value != saved;
                            let draft = value.clone();
                            ui.horizontal(|ui| {
                                if ui.add_enabled(changed, egui::Button::new("Save")).clicked() {
                                    self.cli(
                                        vec![
                                            "config".into(),
                                            "set".into(),
                                            spec.key.clone(),
                                            draft,
                                            "--json".into(),
                                        ],
                                        Purpose::SettingSave(spec.key.clone()),
                                        false,
                                    );
                                }
                                if ui.button("Reset to default").clicked() {
                                    self.cli(
                                        vec![
                                            "config".into(),
                                            "unset".into(),
                                            spec.key.clone(),
                                            "--json".into(),
                                        ],
                                        Purpose::SettingSave(spec.key.clone()),
                                        false,
                                    );
                                }
                            });
                        });
                    });
                }
                if self.group == "advanced" {
                    ui.collapsing("Engine diagnostics", |ui| {
                        for line in &self.engine_logs {
                            ui.monospace(line);
                        }
                    });
                }
                self.task_panel(ui);
            });
    }
}
