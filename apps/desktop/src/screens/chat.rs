use super::card;
use crate::app::VoiceApp;
use crate::theme::{accent, card as card_color, muted, selected};
use eframe::egui;
use eframe::egui::{Color32, RichText};

impl VoiceApp {
    pub(super) fn chat(&mut self, ui: &mut egui::Ui) {
        if self.starting {
            ui.horizontal(|ui| {
                ui.spinner();
                ui.label(&self.status);
            });
            ui.label("Models load when you start a conversation.");
            return;
        }
        if !self.active {
            ui.label("Start or resume a conversation from Home.");
            return;
        }
        if !self.state.work_dir.is_empty() {
            ui.label(format!(
                "Directory: {} ({})",
                self.state.work_dir, self.state.work_dir_source
            ))
            .on_hover_text("This conversation keeps its directory when you switch personas.");
        }
        ui.horizontal_wrapped(|ui| {
            ui.label(RichText::new(&self.state.name).strong().color(accent(ui)));
            ui.label(
                RichText::new(&self.state.session_id)
                    .small()
                    .color(muted(ui)),
            );
            ui.separator();
            ui.selectable_value(&mut self.activity, false, "Chat");
            ui.selectable_value(&mut self.activity, true, "Activity");
            ui.checkbox(&mut self.show_think, "Show thinking");
        });
        ui.horizontal_wrapped(|ui| {
            let mic = if self.state.mic {
                "Pause microphone"
            } else {
                "Resume microphone"
            };
            if ui
                .add_enabled(self.state.voice_available, egui::Button::new(mic))
                .clicked()
            {
                self.action("mic", "", !self.state.mic);
            }
            if ui
                .button(if self.state.muted {
                    "Unmute voice"
                } else {
                    "Mute voice"
                })
                .clicked()
            {
                self.action("audio", "", self.state.muted);
            }
            if ui.button("Interrupt").clicked() {
                self.action("interrupt", "", false);
            }
            if ui
                .button(
                    if self.state.recording == "off" || self.state.recording.is_empty() {
                        "Record talk"
                    } else {
                        "Stop recording"
                    },
                )
                .clicked()
            {
                self.action(
                    "text",
                    if self.state.recording == "off" || self.state.recording.is_empty() {
                        "/record talk"
                    } else {
                        "/record off"
                    },
                    false,
                );
            }
            if ui.button("Compact").clicked() {
                self.action("text", "/compact", false);
            }
        });
        ui.horizontal(|ui| {
            ui.add(
                egui::ProgressBar::new(self.state.input_level.clamp(0., 1.))
                    .desired_width(110.)
                    .text("Mic"),
            );
            ui.add(
                egui::ProgressBar::new(self.state.output_level.clamp(0., 1.))
                    .desired_width(110.)
                    .text("Voice"),
            );
            ui.label(
                RichText::new(&self.state.status).color(if self.state.error {
                    Color32::from_rgb(177, 57, 73)
                } else {
                    muted(ui)
                }),
            );
        });
        let height = (ui.available_height() - 152.).max(100.);
        egui::ScrollArea::vertical()
            .id_salt(if self.activity { "activity" } else { "chat" })
            .stick_to_bottom(true)
            .auto_shrink([false, false])
            .max_height(height)
            .min_scrolled_height(height)
            .show(ui, |ui| {
                ui.set_min_width(ui.available_width());
                if self.activity {
                    if self.state.activity.is_empty() {
                        ui.label("Tool calls, model timing, and work updates appear here.");
                    }
                    for a in &self.state.activity {
                        ui.horizontal_wrapped(|ui| {
                            ui.label(RichText::new(&a.stage).color(accent(ui)));
                            ui.label(&a.detail);
                            if a.milliseconds > 0 {
                                ui.label(
                                    RichText::new(format!("{} ms", a.milliseconds))
                                        .small()
                                        .color(muted(ui)),
                                );
                            }
                        });
                        ui.separator();
                    }
                } else {
                    if self.state.turns.is_empty() {
                        ui.add_space(35.);
                        ui.label(RichText::new("Ready when you are.").size(25.));
                        ui.label(
                            RichText::new("Speak to your agent, hold Talk, or type below.")
                                .color(muted(ui)),
                        );
                    }
                    for turn in &self.state.turns {
                        let user = turn.role == "user";
                        let name = if turn.name.is_empty() {
                            if user { "You" } else { "Assistant" }
                        } else {
                            &turn.name
                        };
                        egui::Frame::new()
                            .fill(if user { selected(ui) } else { card_color(ui) })
                            .inner_margin(16)
                            .corner_radius(18)
                            .show(ui, |ui| {
                                ui.set_min_width((ui.available_width() - 10.).max(0.));
                                ui.label(RichText::new(name).small().strong().color(if user {
                                    muted(ui)
                                } else {
                                    accent(ui)
                                }));
                                if self.show_think && !turn.thinking.is_empty() {
                                    ui.label(
                                        RichText::new(&turn.thinking).italics().color(muted(ui)),
                                    );
                                    ui.separator();
                                }
                                ui.add(egui::Label::new(&turn.content).wrap().selectable(true));
                            });
                    }
                    for result in &self.state.work_results {
                        card(ui, |ui| {
                            ui.strong("Background result");
                            ui.add(egui::Label::new(result).wrap().selectable(true));
                        });
                    }
                    for (i, notice) in self.state.notices.iter().enumerate() {
                        ui.push_id(("notice", i), |ui| {
                            card(ui, |ui| {
                                ui.label(RichText::new(notice).color(muted(ui)));
                            });
                        });
                    }
                    if !self.state.thinking.is_empty() {
                        ui.collapsing("Thinking…", |ui| {
                            ui.label(RichText::new(&self.state.thinking).color(muted(ui)));
                        });
                    }
                    if !self.state.response.is_empty() {
                        card(ui, |ui| {
                            ui.label(RichText::new(&self.state.name).color(accent(ui)));
                            ui.label(&self.state.response);
                        });
                    }
                }
            });
        ui.separator();
        if self.composer.starts_with('/') && !self.composer.contains(' ') {
            ui.horizontal_wrapped(|ui| {
                for command in [
                    "/help",
                    "/settings",
                    "/personas",
                    "/workers",
                    "/work",
                    "/think",
                    "/record",
                    "/speakers",
                    "/compact",
                    "/copy",
                    "/menu",
                ] {
                    if command.starts_with(&self.composer) && ui.small_button(command).clicked() {
                        self.composer = command.into();
                    }
                }
            });
        }
        let composer_id = ui.make_persistent_id("composer");
        let send_key = ui.memory(|m| m.has_focus(composer_id))
            && ui.input_mut(|i| i.consume_key(egui::Modifiers::NONE, egui::Key::Enter));
        let ctx = ui.ctx().clone();
        ui.add(
            egui::TextEdit::multiline(&mut self.composer)
                .desired_rows(2)
                .desired_width(f32::INFINITY)
                .hint_text("Message your agent…  / for commands")
                .id(composer_id),
        );
        ui.horizontal(|ui| {
            let talk = ui.add_enabled(
                self.state.voice_available,
                egui::Button::new(if self.talk_held {
                    "Release to send"
                } else {
                    "Hold to talk"
                })
                .sense(egui::Sense::click_and_drag()),
            );
            if talk.is_pointer_button_down_on() && !self.talk_held {
                self.talk_held = true;
                self.action("talk_down", "", false);
            }
            if self.talk_held && !ui.input(|i| i.pointer.primary_down()) {
                self.talk_held = false;
                self.action("talk_up", "", false);
            }
            ui.label(
                RichText::new("Enter sends · Shift+Enter adds a line · Ctrl+G pauses mic")
                    .small()
                    .color(muted(ui)),
            );
            ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                if ui
                    .add_enabled(
                        !self.composer.trim().is_empty(),
                        egui::Button::new("Send").fill(selected(ui)),
                    )
                    .clicked()
                    || send_key
                {
                    self.submit(&ctx);
                }
            });
        });
    }
}
