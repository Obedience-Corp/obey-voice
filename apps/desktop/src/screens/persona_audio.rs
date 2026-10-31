use crate::app::VoiceApp;
use crate::theme::muted;
use eframe::egui::{self, RichText};

impl VoiceApp {
    pub(super) fn persona_audio_fields(&mut self, ui: &mut egui::Ui) {
        let previous = self.persona_form.audio.clone();
        let form = &mut self.persona_form;
        let mut native = form.audio.native();
        ui.separator();
        ui.label(RichText::new("Audio mode").strong());
        ui.horizontal(|ui| {
            ui.selectable_value(&mut native, false, "Standard");
            ui.selectable_value(&mut native, true, "Native audio · experimental");
        });
        if native != form.audio.native() {
            form.audio.present = true;
            form.audio.mode = if native { "native" } else { "standard" }.into();
        }
        let mut check = false;
        if native {
            ui.label("LFM3 hears recordings and speaks replies through the selected local Ollama runtime.");
            ui.label(RichText::new("Experimental: responses may be inaccurate or claim actions that did not occur.").small().color(muted(ui)));
            super::field(
                ui,
                "Model (empty uses lfm3-audio:2b)",
                &mut form.audio.model,
            );
            super::field(
                ui,
                "Ollama address (empty inherits app default)",
                &mut form.audio.endpoint,
            );
            super::field(
                ui,
                "Native voice (empty uses model default)",
                &mut form.audio.voice,
            );
            ui.label("Spoken controls");
            super::combo(
                ui,
                "native_controls",
                &mut form.audio.controls,
                &["", "standard", "conversation"],
            );
            ui.label("Input transcription");
            super::combo(
                ui,
                "native_transcript",
                &mut form.audio.transcript,
                &["", "auto", "required", "off"],
            );
            ui.checkbox(
                &mut form.audio.retain,
                "Keep input recordings privately with sessions",
            );
            ui.label(RichText::new("Conversation controls skip spoken commands and tools. Standard controls require transcription. Your Standard engines and worker choices stay saved.").small().color(muted(ui)));
            check = ui
                .add_enabled(
                    !form.id.is_empty() && !self.demo,
                    egui::Button::new(if form.audio_check.is_some() {
                        "Cancel setup check"
                    } else {
                        "Check setup quietly"
                    }),
                )
                .clicked();
            if form.id.is_empty() {
                ui.label("Save this persona before checking setup.");
            }
            if form.audio_check.is_some() {
                ui.spinner();
            }
            if !form.audio_notice.is_empty() {
                ui.label(&form.audio_notice);
            }
        }
        let changed = previous != self.persona_form.audio;
        if changed {
            self.stop_persona_audio_check();
            self.stop_persona_preview();
            self.persona_form.audio_notice.clear();
        }
        if check {
            if self.persona_form.audio_check.is_some() {
                self.stop_persona_audio_check();
            } else {
                self.start_persona_audio_check();
            }
        }
    }
}
