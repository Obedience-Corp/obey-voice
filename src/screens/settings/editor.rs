use crate::app::VoiceApp;
use crate::model::{KeySpec, Purpose, display, string};
use crate::screens::card;
use crate::theme::muted;
use eframe::egui::{self, RichText};

impl VoiceApp {
    pub(super) fn setting_editor(&mut self, ui: &mut egui::Ui, spec: &KeySpec) {
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
                                    ui.selectable_value(value, String::new(), "App default");
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
}
