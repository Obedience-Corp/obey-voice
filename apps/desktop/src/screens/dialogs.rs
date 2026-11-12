use crate::app::VoiceApp;
use crate::model::Purpose;
use crate::theme::{accent, muted};
use eframe::egui;
use eframe::egui::{Color32, RichText};
use serde_json::json;

impl VoiceApp {
    pub fn dialogs(&mut self, ctx: &egui::Context) {
        if let Some(session) = self.new_session.clone() {
            let modal = egui::Modal::new(egui::Id::unique("start_conversation"))
                .frame(crate::theme::dialog(self.dark))
                .backdrop_color(Color32::from_black_alpha(85))
                .show(ctx, |ui| {
                    ui.set_width(420.);
                    ui.label(
                        RichText::new("NEW CONVERSATION")
                            .size(11.)
                            .color(accent(ui)),
                    );
                    ui.heading("Start a conversation");
                    ui.label(
                        RichText::new("Choose a persona, then make yourself heard.")
                            .color(muted(ui)),
                    );
                    ui.add_space(12.);
                    ui.label(RichText::new("Persona").strong());
                    egui::ComboBox::from_id_salt("start_persona")
                        .width(ui.available_width())
                        .selected_text(if self.start_persona.is_empty() {
                            "Saved default"
                        } else {
                            &self.start_persona
                        })
                        .show_ui(ui, |ui| {
                            ui.selectable_value(
                                &mut self.start_persona,
                                String::new(),
                                "Saved default",
                            );
                            ui.selectable_value(
                                &mut self.start_persona,
                                ":defaults".into(),
                                "Application defaults",
                            );
                            for p in &self.personas {
                                if !p.inactive {
                                    ui.selectable_value(
                                        &mut self.start_persona,
                                        p.id.clone(),
                                        &p.display_name,
                                    );
                                }
                            }
                        });
                    ui.add_space(12.);
                    ui.label(RichText::new("Voice & audio").strong());
                    let mut microphone = !self.text_only;
                    let mut spoken_replies = !self.silent;
                    ui.checkbox(&mut microphone, "Microphone input")
                        .on_hover_text("Turn off to type your messages instead.");
                    ui.checkbox(&mut spoken_replies, "Spoken replies")
                        .on_hover_text("Turn off to read replies without audio.");
                    self.text_only = !microphone;
                    self.silent = !spoken_replies;
                    ui.add_space(12.);
                    ui.horizontal(|ui| {
                        if ui
                            .add_sized(
                                [260., 42.],
                                egui::Button::new(
                                    RichText::new("Start conversation").color(Color32::WHITE),
                                )
                                .fill(Color32::from_rgb(66, 91, 199)),
                            )
                            .clicked()
                        {
                            self.new_session = None;
                            self.begin_chat(session);
                        }
                        if ui
                            .add_sized([140., 42.], egui::Button::new("Cancel"))
                            .clicked()
                        {
                            self.new_session = None;
                        }
                    });
                });
            if modal.should_close() {
                self.new_session = None;
            }
        }
        self.persona_dialog(ctx);
        if let Some((message, args)) = self.confirm.clone() {
            egui::Window::new("Confirm")
                .collapsible(false)
                .resizable(false)
                .show(ctx, |ui| {
                    ui.label(message);
                    ui.horizontal(|ui| {
                        if ui.button("Confirm").clicked() {
                            self.confirm = None;
                            if args[0] == "@session_delete" {
                                self.request(
                                    "session_delete",
                                    json!({"session_id":args[1]}),
                                    Purpose::Mutation("Conversation deleted".into()),
                                );
                            } else if args[0] == "@enroll_remove" {
                                self.action("enroll_remove", &args[1], false);
                            } else if args[0] == super::settings::storage::MOVE_ACTION {
                                self.models_move_to.clear();
                                self.start_task(
                                    "Move models",
                                    super::settings::storage::move_arguments(&args[1]),
                                );
                            } else if args[0] == "@workers_stop" {
                                self.action("workers_stop", "", false);
                            } else {
                                self.cli(args, Purpose::Mutation("Deleted".into()), false);
                            }
                        }
                        if ui.button("Cancel").clicked() {
                            self.confirm = None;
                        }
                    });
                });
        }
    }
}
