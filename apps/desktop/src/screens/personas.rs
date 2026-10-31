use super::card;
use crate::app::VoiceApp;
use crate::model::{PersonaForm, Purpose};
use crate::theme::{accent, muted};
use eframe::egui;
use eframe::egui::RichText;

impl VoiceApp {
    pub(super) fn persona_list(&mut self, ui: &mut egui::Ui) {
        if self.active && !self.state.persona_message.is_empty() {
            ui.add(
                egui::Label::new(&self.state.persona_message)
                    .wrap()
                    .selectable(true),
            );
        }
        ui.horizontal(|ui| {
            ui.label(
                RichText::new(if self.active {
                    "Choose the responder for the next turn. Your conversation stays open."
                } else {
                    "Select a default, or create an agent with its own prompt, model, and voice."
                })
                .color(muted(ui)),
            );
        });
        ui.horizontal(|ui| {
            if ui.button("+ Create persona").clicked() {
                self.stop_persona_preview();
                self.stop_persona_audio_check();
                self.persona_form = PersonaForm {
                    open: true,
                    prompt: "You are {agent_name}, a voice assistant.".into(),
                    ..Default::default()
                };
                self.load_persona_directories();
            }
            if ui.button("Refresh").clicked() {
                self.stop_persona_preview();
                self.command(&["persona", "list", "--json"], Purpose::Personas);
            }
        });
        egui::ScrollArea::vertical().show(ui, |ui| {
            for p in self.personas.clone() {
                let native = p.audio["mode"].as_str() == Some("native");
                card(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.label(RichText::new(&p.display_name).size(20.).strong());
                        if p.active {
                            ui.label(RichText::new("DEFAULT").small().color(accent(ui)));
                        }
                        if p.inactive {
                            ui.label("Inactive");
                        }
                    });
                    let details = if native {
                        let model = p.audio["model"]
                            .as_str()
                            .filter(|value| !value.is_empty())
                            .unwrap_or("lfm3-audio:2b");
                        let voice = p.audio["voice"]
                            .as_str()
                            .filter(|value| !value.is_empty())
                            .unwrap_or("model default voice");
                        format!("{} · {} · {}", p.id, model, voice)
                    } else {
                        format!("{} · {} · {}", p.id, p.provider, p.voice)
                    };
                    ui.label(RichText::new(details).color(muted(ui)));
                    if native {
                        ui.label("Native audio · experimental");
                    }
                    ui.horizontal_wrapped(|ui| {
                        let playing = self.persona_form.preview_persona == p.id
                            && self.persona_form.preview_request.is_some();
                        if ui
                            .add_enabled(
                                self.connected && !self.demo && !native,
                                egui::Button::new(if playing {
                                    "Stop preview"
                                } else {
                                    "Preview voice"
                                }),
                            )
                            .clicked()
                        {
                            self.preview_saved_persona(&p.id);
                        }
                        if ui
                            .add_enabled(
                                !p.inactive,
                                egui::Button::new(if self.active {
                                    "Use in this chat"
                                } else {
                                    "Make default"
                                }),
                            )
                            .clicked()
                        {
                            self.stop_persona_preview();
                            if self.active {
                                self.action("persona", &p.id, false);
                            } else {
                                self.cli(
                                    vec![
                                        "persona".into(),
                                        "use".into(),
                                        p.id.clone(),
                                        "--json".into(),
                                    ],
                                    Purpose::Mutation("Default persona saved".into()),
                                    false,
                                );
                            }
                        }
                        if ui.button("Edit").clicked() {
                            self.stop_persona_preview();
                            self.cli(
                                vec![
                                    "persona".into(),
                                    "show".into(),
                                    p.id.clone(),
                                    "--with-prompt".into(),
                                    "--json".into(),
                                ],
                                Purpose::PersonaLoad,
                                false,
                            );
                        }
                        if ui
                            .add_enabled(
                                !p.active,
                                egui::Button::new(if p.inactive { "Enable" } else { "Disable" }),
                            )
                            .clicked()
                        {
                            self.stop_persona_preview();
                            self.cli(
                                vec![
                                    "persona".into(),
                                    if p.inactive { "enable" } else { "disable" }.into(),
                                    p.id.clone(),
                                    "--json".into(),
                                ],
                                Purpose::Mutation("Persona availability saved".into()),
                                false,
                            );
                        }
                        if ui
                            .add_enabled(!p.builtin, egui::Button::new("Delete…"))
                            .clicked()
                        {
                            self.stop_persona_preview();
                            self.confirm = Some((
                                format!("Delete persona {} and its owned prompt?", p.display_name),
                                vec![
                                    "persona".into(),
                                    "delete".into(),
                                    p.id.clone(),
                                    "--yes".into(),
                                    "--json".into(),
                                ],
                            ));
                        }
                    });
                    if self.persona_form.preview_persona == p.id
                        && !self.persona_form.preview_notice.is_empty()
                    {
                        let color = if self.persona_form.preview_error {
                            egui::Color32::from_rgb(194, 74, 87)
                        } else {
                            muted(ui)
                        };
                        ui.colored_label(color, &self.persona_form.preview_notice);
                    }
                });
            }
        });
    }
}
