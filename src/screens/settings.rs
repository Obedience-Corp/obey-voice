mod editor;
mod layout;

use super::{card, field};
use crate::app::VoiceApp;
use crate::model::{Purpose, Screen};
use crate::theme::muted;
use eframe::egui;
use eframe::egui::RichText;

impl VoiceApp {
    pub(super) fn settings(&mut self, ui: &mut egui::Ui) {
        egui::Panel::left("settings_sections")
            .exact_size(130.)
            .resizable(false)
            .frame(egui::Frame::NONE)
            .show(ui, |ui| {
                egui::ScrollArea::vertical()
                    .id_salt("settings_sections_scroll")
                    .show(ui, |ui| {
                        for (index, &(id, title)) in layout::SECTIONS.iter().enumerate() {
                            if index == 9 {
                                ui.separator();
                                ui.label("More settings");
                            }
                            if ui
                                .selectable_value(&mut self.group, id.into(), title)
                                .changed()
                            {
                                self.search.clear();
                            }
                        }
                    });
            });
        egui::CentralPanel::default()
            .frame(egui::Frame::NONE)
            .show(ui, |ui| {
                self.settings_content(ui);
            });
    }

    fn settings_content(&mut self, ui: &mut egui::Ui) {
        ui.heading(layout::title(&self.group));
        if ui
            .add_enabled(!self.active, egui::Button::new("Run guided setup"))
            .clicked()
        {
            self.open_setup();
        }
        if self.group == "workers" && ui.button("Open worker policy and running jobs").clicked() {
            self.navigate(Screen::Workers);
        }
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
        let provider = self.values["brain_provider"]["value"]
            .as_str()
            .unwrap_or("ollama");
        let keys = layout::visible_keys(&self.schema, &self.group, &self.search, provider);
        egui::ScrollArea::vertical()
            .id_salt(("settings_scroll", &self.group, &self.search))
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
                let claude: Vec<_> = keys.iter().filter(|s| self.search.is_empty() && self.group == "advanced" && s.key.starts_with("claude_")).cloned().collect();
                for spec in keys.iter().filter(|s| !claude.iter().any(|c| c.key == s.key)) {
                    if !self.search.is_empty() {
                        ui.label(RichText::new(layout::title(layout::section(spec))).small().color(muted(ui)));
                    }
                    self.setting_editor(ui, spec);
                }
                if !claude.is_empty() {
                    ui.collapsing("Claude session management", |ui| {
                        ui.label("Only used by the Claude brain provider. These limits do not affect Ollama, Grok, or Cursor.");
                        for spec in &claude { self.setting_editor(ui, spec); }
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
