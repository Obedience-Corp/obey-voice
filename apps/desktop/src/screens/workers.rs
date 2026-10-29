use super::{card, field};
use crate::app::VoiceApp;
use crate::model::{Purpose, Screen, string};
use crate::theme::{accent, muted};
use eframe::egui;
use eframe::egui::{Color32, RichText};
use serde_json::{Value, json};

impl VoiceApp {
    pub(super) fn workers(&mut self, ui: &mut egui::Ui) {
        ui.collapsing("Worker models, limits, and spending policy", |ui| {
            ui.label("Assignments and limits apply through the shared Obey Voice policy. Saving checks for concurrent edits.");
            egui::ScrollArea::vertical().id_salt("worker_policy").max_height(360.).show(ui, |ui| {
                edit_policy(ui, "Workers", &mut self.worker_config);
            });
            ui.horizontal(|ui| {
                if ui.add_enabled(!self.worker_config.is_null(), egui::Button::new("Save worker policy")).clicked() {
                    self.request("worker_config_save", json!({"config":self.worker_config,"revision":self.worker_revision}), Purpose::WorkerConfig);
                }
                if ui.button("Reload policy").clicked() { self.request("worker_config_get", json!({}), Purpose::WorkerConfig); }
            });
        });
        if !self.active {
            ui.label("Worker controls become available inside a conversation. Configure workers in Settings.");
            if ui.button("Open settings").clicked() {
                self.navigate(Screen::Settings);
            }
            return;
        }
        let mut enabled = self.state.workers_enabled;
        ui.horizontal(|ui| {
            if ui
                .checkbox(&mut enabled, "Allow new background workers")
                .changed()
            {
                self.action("workers_enabled", "", enabled);
            }
            if ui.button("Stop all…").clicked() {
                self.confirm = Some((
                    "Stop every running and queued task?".into(),
                    vec!["@workers_stop".into()],
                ));
            }
        });
        ui.label(
            RichText::new(
                "Turning workers off affects new tasks. Existing tasks continue until stopped.",
            )
            .small()
            .color(muted(ui)),
        );
        egui::ScrollArea::vertical().show(ui, |ui| {
            if self.state.workers.is_empty() {
                ui.label("No background tasks in this conversation.");
            }
            for w in self.state.workers.clone() {
                let id = string(&w, "ID");
                card(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.strong(string(&w, "Task"));
                        ui.label(RichText::new(string(&w, "State")).color(accent(ui)));
                    });
                    ui.label(
                        RichText::new(format!(
                            "{} · {}",
                            string(&w, "Provider"),
                            string(&w, "Model")
                        ))
                        .color(muted(ui)),
                    );
                    ui.label(string(&w, "Activity"));
                    ui.horizontal(|ui| {
                        if ui.button("Details").clicked() {
                            self.selected_worker = Some(id.clone());
                        }
                        if ui.button("Stop task").clicked() {
                            self.action("worker_stop", &id, false);
                        }
                        for action in ["dismiss", "speak", "replay"] {
                            if ui.small_button(action).clicked() {
                                self.action("text", &format!("/work {action} {id}"), false);
                            }
                        }
                    });
                    if self.selected_worker.as_ref() == Some(&id) {
                        ui.separator();
                        if let Some(turns) = self.state.worker_threads.get(&id) {
                            for turn in turns {
                                ui.strong(if turn.role == "user" {
                                    "Request"
                                } else {
                                    "Worker"
                                });
                                ui.add(egui::Label::new(&turn.content).wrap().selectable(true));
                                self.media.show(ui, &turn.attachments, false);
                            }
                        } else {
                            ui.label(string(&w, "Result"));
                        }
                        ui.colored_label(Color32::from_rgb(177, 57, 73), string(&w, "Err"));
                        ui.label(string(&w, "Termination"));
                    }
                });
            }
        });
    }
}

// Native controls for the versioned upstream policy; upstream remains the
// validator, revision authority, and store.
fn edit_policy(ui: &mut egui::Ui, key: &str, value: &mut Value) {
    ui.push_id(key, |ui| {
        match value {
            Value::Object(object) => { ui.collapsing(key.replace('_', " "), |ui| {
                for (name, value) in object.iter_mut() {
                    if name != "revision" && name != "schema_version" { edit_policy(ui, name, value); }
                }
            }); },
            Value::Bool(v) => { ui.checkbox(v, key.replace('_', " ")); },
            Value::Number(v) => { ui.horizontal(|ui| { ui.label(key.replace('_', " ")); let mut n = v.as_i64().unwrap_or(0); if ui.add(egui::DragValue::new(&mut n).range(0..=i64::MAX)).changed() { *v = n.into(); } }); },
            Value::String(v) => { field(ui, &key.replace('_', " "), v); },
            Value::Null if ui.button(format!("Set {}", key.replace('_', " "))).clicked() => {
                *value = if key == "assignment" { json!({"provider":"ollama","model":"","endpoint_ref":null,"account_ref":null,"billing":"local"}) }
                    else if key.ends_with("_ref") { json!("") } else { json!(0) };
            },
            _ => {}
        }
        if !value.is_null() && ["assignment","endpoint_ref","account_ref","output_tokens","charge_micro_usd","daily_charge_micro_usd"].contains(&key) && ui.small_button("Unset").clicked() { *value = Value::Null; }
    });
}
