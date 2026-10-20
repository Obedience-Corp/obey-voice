use super::card;
use crate::app::VoiceApp;
use crate::model::{PersonaForm, Purpose};
use crate::theme::{accent, muted};
use eframe::egui;
use eframe::egui::RichText;

impl VoiceApp {
    pub(super) fn persona_list(&mut self, ui: &mut egui::Ui) {
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
                self.persona_form = PersonaForm {
                    open: true,
                    prompt: "You are {agent_name}, a voice assistant.".into(),
                    ..Default::default()
                };
            }
            if ui.button("Refresh").clicked() {
                self.command(&["persona", "list", "--json"], Purpose::Personas);
            }
        });
        egui::ScrollArea::vertical().show(ui, |ui| {
            for p in self.personas.clone() {
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
                    ui.label(
                        RichText::new(format!("{} · {} · {}", p.id, p.provider, p.voice))
                            .color(muted(ui)),
                    );
                    ui.horizontal_wrapped(|ui| {
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
                });
            }
        });
    }
}
