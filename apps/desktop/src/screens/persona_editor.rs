use super::provider_choices::persona_choices;
use crate::app::VoiceApp;
use crate::theme::{accent, muted, palette};
use eframe::egui::{self, Color32, RichText};

impl VoiceApp {
    pub(super) fn persona_dialog(&mut self, ctx: &egui::Context) {
        if !self.persona_form.open {
            return;
        }
        let creating = self.persona_form.id.is_empty();
        let title = if creating {
            "Create persona"
        } else {
            "Edit persona"
        };
        let modal = egui::Modal::new(egui::Id::unique("persona_editor"))
            .frame(crate::theme::dialog(self.dark).inner_margin(24))
            .backdrop_color(Color32::from_black_alpha(90))
            .show(ctx, |ui| {
                ui.set_width((ctx.content_rect().width() - 96.).clamp(280., 580.));
                ui.spacing_mut().item_spacing = egui::vec2(12., 8.);
                ui.spacing_mut().button_padding = egui::vec2(12., 7.);
                ui.label(
                    RichText::new("PERSONA")
                        .size(11.)
                        .strong()
                        .color(accent(ui)),
                );
                ui.label(RichText::new(title).size(26.).strong());
                ui.label(
                    RichText::new("Give your assistant a name, a voice, and a point of view.")
                        .color(muted(ui)),
                );
                ui.add_space(10.);

                // Only the form scrolls. The title and actions stay reachable at
                // the minimum window size and when optional sections expand.
                egui::ScrollArea::vertical()
                    .id_salt("persona_editor_body")
                    .max_height((ctx.content_rect().height() - 270.).max(120.))
                    .auto_shrink([false, true])
                    .show(ui, |ui| {
                        if !self.persona_form.save_error.is_empty() {
                            ui.colored_label(
                                Color32::from_rgb(194, 74, 87),
                                &self.persona_form.save_error,
                            );
                        }
                        self.persona_fields(ui);
                    });

                ui.add_space(8.);
                ui.separator();
                ui.horizontal(|ui| {
                    ui.label(
                        RichText::new("Used for new conversations")
                            .small()
                            .color(muted(ui)),
                    );
                    ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                        let save = egui::Button::new(
                            RichText::new(if creating {
                                "Create persona"
                            } else {
                                "Save changes"
                            })
                            .color(Color32::WHITE),
                        )
                        .fill(Color32::from_rgb(66, 91, 199));
                        if ui
                            .add_enabled(!self.persona_form.name.trim().is_empty(), save)
                            .clicked()
                        {
                            self.save_persona();
                        }
                        if ui.button("Cancel").clicked() {
                            self.persona_form.open = false;
                        }
                    });
                });
            });
        if modal.should_close() {
            self.persona_form.open = false;
        }
        if !self.persona_form.open {
            self.stop_persona_preview();
        }
    }

    fn persona_fields(&mut self, ui: &mut egui::Ui) {
        let brains = persona_choices(&self.schema, "brain_provider", &self.persona_form.brain);
        let speech = persona_choices(&self.schema, "tts_provider", &self.persona_form.tts);
        let can_preview = self.connected && !self.demo;
        let f = &mut self.persona_form;
        let previous_voice = (f.tts.clone(), f.voice.clone(), f.tier.clone());
        let mut preview_clicked = false;
        ui.label(RichText::new("Name").strong());
        ui.add(
            egui::TextEdit::singleline(&mut f.name)
                .hint_text("e.g. Research companion")
                .desired_width(f32::INFINITY),
        );
        ui.add_space(8.);
        ui.columns(2, |columns| {
            section(&mut columns[0], "Intelligence", |ui| {
                label(ui, "Provider");
                provider(ui, "persona_brain", &mut f.brain, &brains);
                label(ui, "Model");
                ui.add(
                    egui::TextEdit::singleline(&mut f.model)
                        .hint_text("Use provider default")
                        .desired_width(f32::INFINITY),
                );
            });
            section(&mut columns[1], "Voice", |ui| {
                label(ui, "Speech engine");
                provider(ui, "persona_speech", &mut f.tts, &speech);
                label(ui, "Voice");
                ui.add(
                    egui::TextEdit::singleline(&mut f.voice)
                        .hint_text("Use engine default")
                        .desired_width(f32::INFINITY),
                );
                if f.tts.is_empty() || f.tts == "qwen3-tts" {
                    label(ui, "Qwen model tier");
                    super::combo(ui, "persona_tier", &mut f.tier, &["", "0.6b", "1.7b"]);
                }
                let previewing = f.preview_request.is_some();
                ui.horizontal(|ui| {
                    preview_clicked = ui
                        .add_enabled(
                            can_preview,
                            egui::Button::new(if previewing {
                                "Stop preview"
                            } else {
                                "Preview voice"
                            }),
                        )
                        .on_hover_text("Hear this voice before saving the persona")
                        .clicked();
                    if previewing {
                        ui.spinner();
                    }
                });
                if !f.preview_notice.is_empty() {
                    ui.label(
                        RichText::new(&f.preview_notice)
                            .small()
                            .color(if f.preview_error {
                                Color32::from_rgb(194, 74, 87)
                            } else {
                                muted(ui)
                            }),
                    );
                }
            });
        });
        ui.add_space(8.);
        ui.label(RichText::new("Instructions").strong());
        if f.structured {
            ui.label(RichText::new("This persona uses a structured prompt. Edit its source document to preserve the sections.").small().color(muted(ui)));
        } else {
            ui.add(
                egui::TextEdit::multiline(&mut f.prompt)
                    .hint_text("Describe how this assistant should think and respond…")
                    .desired_rows(4)
                    .desired_width(f32::INFINITY),
            );
        }
        ui.add_space(4.);
        let folder = if f.starting_directory.is_empty() {
            "App default"
        } else {
            "Custom folder"
        };
        let voice_changed = previous_voice != (f.tts.clone(), f.voice.clone(), f.tier.clone());
        if voice_changed {
            self.stop_persona_preview();
            self.persona_form.preview_notice.clear();
            self.persona_form.preview_error = false;
        }
        if preview_clicked {
            self.toggle_persona_preview();
        }
        egui::CollapsingHeader::new(format!("Starting folder · {folder}"))
            .id_salt("persona_starting_folder")
            .show(ui, |ui| self.persona_directory_editor(ui));
    }
}

fn label(ui: &mut egui::Ui, text: &str) {
    ui.label(RichText::new(text).small().color(muted(ui)));
}

fn provider(ui: &mut egui::Ui, id: &str, value: &mut String, choices: &[String]) {
    egui::ComboBox::from_id_salt(id)
        .width(ui.available_width())
        .selected_text(if value.is_empty() {
            "App default"
        } else {
            value.as_str()
        })
        .show_ui(ui, |ui| {
            for choice in choices {
                ui.selectable_value(
                    value,
                    choice.clone(),
                    if choice.is_empty() {
                        "App default"
                    } else {
                        choice
                    },
                );
            }
        });
}

fn section(ui: &mut egui::Ui, title: &str, contents: impl FnOnce(&mut egui::Ui)) {
    let colors = palette(ui.visuals().dark_mode);
    egui::Frame::new()
        .fill(colors.surface)
        .stroke(egui::Stroke::new(1., colors.border))
        .corner_radius(14)
        .inner_margin(14)
        .show(ui, |ui| {
            ui.set_width(ui.available_width());
            ui.label(RichText::new(title).strong());
            contents(ui);
        });
}
