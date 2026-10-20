use super::card;
use crate::app::VoiceApp;
use crate::model::Screen;
use crate::theme::{muted, selected};
use eframe::egui;
use eframe::egui::RichText;

impl VoiceApp {
    pub(super) fn home(&mut self, ui: &mut egui::Ui) {
        egui::ScrollArea::vertical().show(ui, |ui| {
            card(ui, |ui| {
                let r = ui.available_rect_before_wrap();
                if r.width() > 600. {
                    crate::theme::orb(
                        ui.painter(),
                        egui::pos2(r.right() - 88., r.top() + 84.),
                        65.,
                        ui.visuals().dark_mode,
                    );
                }
                ui.add_space(12.);
                ui.label(
                    RichText::new("A SPACE TO THINK OUT LOUD")
                        .size(10.)
                        .color(muted(ui)),
                );
                ui.add_space(10.);
                ui.label(RichText::new("What’s on your mind?").size(34.).strong());
                ui.add_space(6.);
                ui.label(
                    RichText::new("Start with your voice. Pick up wherever you left off.")
                        .color(muted(ui)),
                );
                ui.add_space(20.);
                ui.horizontal(|ui| {
                    if ui
                        .add_enabled(
                            self.connected && !self.starting,
                            egui::Button::new(RichText::new("+ New conversation").strong())
                                .fill(selected(ui)),
                        )
                        .clicked()
                    {
                        self.new_session = Some(String::new());
                    }
                    if let Some(s) = self.sessions.first() {
                        let id = s.id.clone();
                        if ui
                            .add_enabled(!self.starting, egui::Button::new("Continue latest"))
                            .clicked()
                        {
                            self.start_persona.clear();
                            self.begin_chat(id);
                        }
                    }
                });
                ui.add_space(16.);
            });
            ui.add_space(24.);
            ui.label(
                RichText::new("RECENT CONVERSATIONS")
                    .small()
                    .color(muted(ui)),
            );
            if self.sessions.is_empty() {
                ui.label("Your conversations will appear here after your first chat.");
            }
            for s in self.sessions.clone().into_iter().take(3) {
                card(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.vertical(|ui| {
                            ui.strong(if s.summary.is_empty() {
                                "Untitled conversation"
                            } else {
                                &s.summary
                            });
                            ui.label(
                                RichText::new(format!("{} · {}", s.provider, s.updated_at))
                                    .small()
                                    .color(muted(ui)),
                            );
                        });
                        ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                            if ui.button("Resume").clicked() {
                                self.start_persona.clear();
                                self.begin_chat(s.id);
                            }
                        });
                    });
                });
            }
            ui.add_space(20.);
            ui.columns(2, |columns| {
                card(&mut columns[0], |ui| {
                    ui.strong("Meeting");
                    ui.label("Live transcript, notes, bookmarks, and speaker labels.");
                    if ui.button("Set up a recording").clicked() {
                        self.navigate(Screen::Meeting);
                    }
                });
                card(&mut columns[1], |ui| {
                    ui.strong("Personas");
                    ui.label("Choose a voice, model, and identity for the next conversation.");
                    if ui.button("Browse personas").clicked() {
                        self.navigate(Screen::Personas);
                    }
                });
            });
        });
    }
    pub(super) fn session_list(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            ui.add(egui::TextEdit::singleline(&mut self.search).hint_text("Search conversations…"));
            if ui.button("Refresh").clicked() {
                self.refresh();
            }
        });
        egui::ScrollArea::vertical().show(ui, |ui| {
            for session in self.sessions.clone() {
                if !format!("{} {}", session.summary, session.id)
                    .to_lowercase()
                    .contains(&self.search.to_lowercase())
                {
                    continue;
                }
                card(ui, |ui| {
                    ui.strong(if session.summary.is_empty() {
                        "Untitled conversation"
                    } else {
                        &session.summary
                    });
                    ui.label(
                        RichText::new(format!(
                            "{} · {} · {} turns",
                            session.updated_at,
                            session.model,
                            session.turns.as_ref().map_or(0, Vec::len)
                        ))
                        .color(muted(ui)),
                    );
                    ui.horizontal(|ui| {
                        if ui.button("Resume").clicked() {
                            self.start_persona.clear();
                            self.begin_chat(session.id.clone());
                        }
                        if ui.button("Delete…").clicked() {
                            self.confirm = Some((
                                format!("Delete conversation {}?", session.id),
                                vec!["@session_delete".into(), session.id.clone()],
                            ));
                        }
                    });
                });
            }
        });
    }
}
