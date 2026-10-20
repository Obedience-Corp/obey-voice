use super::{card, field};
use crate::app::VoiceApp;
use crate::model::{Purpose, string};
use crate::theme::{accent, muted};
use eframe::egui;
use eframe::egui::{Color32, RichText};
use serde_json::{Value, json};

impl VoiceApp {
    pub(super) fn meeting(&mut self, ui: &mut egui::Ui) {
        egui::ScrollArea::vertical().show(ui, |ui| {
            if !self.meeting_active {
                card(ui, |ui| {
                    field(ui, "Meeting title", &mut self.meeting_title);
                    ui.horizontal(|ui| {
                        if ui
                            .add_enabled(
                                !self.starting && !self.meeting_title.trim().is_empty(),
                                egui::Button::new("Start recording"),
                            )
                            .clicked()
                        {
                            self.meeting_lines.clear();
                            self.meeting_partial.clear();
                            self.meeting_notes.clear();
                            self.meeting_level = 0.;
                            self.meeting_phase = "Preparing microphone…".into();
                            self.meeting_result = Value::Null;
                            self.starting = true;
                            self.request(
                                "meeting_start",
                                json!({"text":self.meeting_title}),
                                Purpose::MeetingStart,
                            );
                            if self.demo {
                                self.starting = false;
                                self.meeting_active = true;
                                self.meeting_phase = "Preview · no microphone".into();
                                self.meeting_lines = vec!["00:04  Let’s capture decisions as we go.".into()];
                                self.meeting_partial = "We should start with…".into();
                            }
                        }
                        if ui
                            .add_enabled(!self.starting, egui::Button::new("Resume last meeting"))
                            .clicked()
                        {
                            self.starting = true;
                            self.request(
                                "meeting_start",
                                json!({"resume":"last"}),
                                Purpose::MeetingStart,
                            );
                        }
                    });
                });
            } else {
                card(ui, |ui| {
                    ui.label(RichText::new(if self.meeting_title.is_empty() { "Meeting" } else { &self.meeting_title }).size(22.));
                    ui.horizontal(|ui| {
                        ui.label(RichText::new(if self.starting { "Saving meeting…" } else if self.meeting_paused { "Paused" } else { "● Recording" }).color(accent(ui)));
                        if !self.meeting_paused { ui.label(&self.meeting_phase); }
                        ui.add(egui::ProgressBar::new(self.meeting_level.clamp(0., 1.)).desired_width(100.).text("Mic"));
                    });
                    ui.add_enabled_ui(!self.starting, |ui| {
                        ui.horizontal(|ui| {
                            if ui.button(if self.meeting_paused { "Resume" } else { "Pause" }).clicked() {
                                self.request(if self.meeting_paused { "meeting_resume" } else { "meeting_pause" }, json!({}), Purpose::MeetingPaused(!self.meeting_paused));
                                if self.demo { self.meeting_paused = !self.meeting_paused; }
                            }
                            if ui.button("★ Bookmark").clicked() {
                                self.request("meeting_bookmark", json!({"text":self.meeting_note}), Purpose::MeetingNote(format!("★ {}", self.meeting_note)));
                            }
                            if ui.button("Stop and review").clicked() {
                                self.starting = !self.demo;
                                self.request("meeting_stop", json!({}), Purpose::MeetingStop);
                                if self.demo { self.meeting_active = false; }
                            }
                        });
                    });
                });
                ui.add_space(12.);
                ui.columns(2, |cols| {
                    card(&mut cols[0], |ui| {
                        ui.strong("Live transcript");
                        egui::ScrollArea::vertical().id_salt("live_meeting_transcript").max_height(340.).stick_to_bottom(true).show(ui, |ui| {
                            ui.set_min_height(180.);
                            if self.meeting_lines.is_empty() && self.meeting_partial.is_empty() { ui.label(RichText::new("Listening. Your transcript will appear here as you speak.").color(muted(ui))); }
                            for line in &self.meeting_lines { ui.add(egui::Label::new(line).wrap().selectable(true)); }
                            if !self.meeting_partial.is_empty() { ui.label(RichText::new(&self.meeting_partial).italics().color(muted(ui))); }
                        });
                    });
                    card(&mut cols[1], |ui| {
                        ui.strong("Your notes");
                        ui.label(RichText::new("Capture decisions, questions, and follow-ups.").small().color(muted(ui)));
                        ui.add(egui::TextEdit::multiline(&mut self.meeting_note).hint_text("Add a timestamped note…").desired_rows(4).desired_width(f32::INFINITY));
                        let pending = self.pending.values().any(|p| matches!(p, Purpose::MeetingNote(_)));
                        if ui.add_enabled(!self.starting && !pending && !self.meeting_note.trim().is_empty(), egui::Button::new("Save note")).clicked() {
                            let note = self.meeting_note.clone();
                            self.request("meeting_note", json!({"text":note}), Purpose::MeetingNote(note.clone()));
                            if self.demo { self.meeting_notes.push(note); self.meeting_note.clear(); }
                        }
                        egui::ScrollArea::vertical().id_salt("live_meeting_notes").max_height(180.).stick_to_bottom(true).show(ui, |ui| {
                            for note in &self.meeting_notes { ui.add(egui::Label::new(format!("✓ {note}")).wrap().selectable(true)); }
                        });
                    });
                });
            }
            if self.meeting_active { return; }
            if !self.meeting_result.is_null() {
                card(ui, |ui| {
                    ui.strong("Meeting saved");
                    if let Some(document) = self.meeting_result["document"].as_str() {
                        ui.add(egui::Label::new(document).wrap().selectable(true));
                    } else {
                        ui.label(&self.meeting_path);
                        if ui.button("Read transcript").clicked() {
                            self.cli(
                                vec![
                                    "meeting".into(),
                                    "show".into(),
                                    self.meeting_path.clone(),
                                    "--json".into(),
                                ],
                                Purpose::MeetingDocument,
                                false,
                            );
                        }
                    }
                    ui.label("File notes to a campaign");
                    ui.horizontal(|ui| {
                        egui::ComboBox::from_id_salt("meeting_campaign")
                            .selected_text(if self.route_to.is_empty() { "Choose a destination" } else { &self.route_to })
                            .show_ui(ui, |ui| {
                                for campaign in &self.campaigns {
                                    let name = string(campaign, "name");
                                    if !name.is_empty() { ui.selectable_value(&mut self.route_to, format!("camp:{name}"), name); }
                                }
                            });
                        if ui.button("Refresh destinations").clicked() { self.command(&["--campaigns"], Purpose::Campaigns); }
                    });
                    if let Some(error) = &self.campaign_error { ui.label(RichText::new(format!("Campaign discovery: {error}")).color(Color32::LIGHT_RED)); }
                    ui.collapsing("Other destination", |ui| { field(ui, "Destination ID from your routing config", &mut self.route_to); });
                    if ui.add_enabled(!self.route_to.trim().is_empty() && self.task.is_none(), egui::Button::new("File meeting notes")).clicked() {
                        self.logs.clear();
                        self.task_title = "Route meeting".into();
                        let id = self.cli(vec!["meeting".into(), "route".into(), self.meeting_path.clone(), "--to".into(), self.route_to.clone(), "--json".into()], Purpose::MeetingRoute, false);
                        if !self.demo { self.task = Some(id); }
                        self.status = "Filing meeting notes…".into();
                    }
                });
            }
            ui.add_space(12.);
            ui.strong("Meeting history");
            for meeting in self.meetings.clone() {
                card(ui, |ui| {
                    ui.strong(string(&meeting, "description"));
                    ui.label(string(&meeting, "started_at"));
                    if ui.button("Review").clicked() {
                        self.meeting_path = string(&meeting, "bundle");
                        let id = string(&meeting, "id");
                        self.cli(
                            vec!["meeting".into(), "show".into(), id, "--json".into()],
                            Purpose::MeetingDocument,
                            false,
                        );
                    }
                });
            }
            self.task_panel(ui);
        });
    }
}
