use crate::app::VoiceApp;
use crate::theme::muted;
use eframe::egui;

impl VoiceApp {
    pub(super) fn audio_output_controls(&mut self, ui: &mut egui::Ui) {
        if !self.state.output_reset_available && self.state.output_capture.is_none() {
            return;
        }
        ui.horizontal_wrapped(|ui| {
            if ui.add_enabled(
                self.state.output_reset_available && !self.state.output_action_busy,
                egui::Button::new("Reset audio output"),
            ).on_hover_text("Stop the current spoken reply and reopen the output on the next reply. Your conversation stays open.").clicked() {
                self.action("audio_reset", "", false);
            }
            if let Some(capture) = &self.state.output_capture {
                let active = capture.active;
                let label = if active { "Stop diagnostic capture" } else { "Capture audio diagnostics" };
                if ui.add_enabled(!self.state.output_action_busy, egui::Button::new(label))
                    .on_hover_text("Save source and rendered speech locally for up to 60 seconds or 32 MiB. Keeps three captures; removes expired captures after 24 hours while running or at the next capture.")
                    .clicked() {
                    self.action(if active { "audio_capture_stop" } else { "audio_capture_start" }, "", false);
                }
            }
            if self.state.output_action_busy {
                ui.spinner();
            }
        });
        if let Some(status) = &self.state.output_status {
            if !status.last_error.is_empty() {
                ui.colored_label(ui.visuals().error_fg_color, format!("Audio output unavailable: {}. Check the selected device, then reset audio output to retry.", status.last_error));
            } else if status.state == "recovering" || status.state == "opening" {
                ui.label("Reopening audio output…");
            }
        }
        if !self.state.output_message.is_empty() {
            let color = if self.state.output_error {
                ui.visuals().error_fg_color
            } else {
                muted(ui)
            };
            ui.colored_label(color, &self.state.output_message);
        }
        if let Some(capture) = &self.state.output_capture {
            if capture.active {
                ui.label("● Diagnostic audio capture is active (up to 60 seconds)");
            } else if !capture.limit_reason.is_empty() {
                let reason = match capture.limit_reason.as_str() {
                    "duration_limit" => "Diagnostic capture reached its 60-second limit",
                    "byte_limit" => "Diagnostic capture reached its size limit",
                    "write_error" => {
                        "Diagnostic capture could not save all audio. Check free disk space and capture again."
                    }
                    "format_update_failed" => {
                        "Diagnostic capture stopped because the output format changed. Start another capture."
                    }
                    _ => "Diagnostic capture stopped",
                };
                ui.label(reason);
            }
            if !capture.directory.is_empty() {
                ui.horizontal_wrapped(|ui| {
                    ui.label("Capture folder:");
                    ui.add(egui::Label::new(&capture.directory).selectable(true));
                    if ui.button("Copy capture path").clicked() {
                        ui.ctx().copy_text(capture.directory.clone());
                    }
                });
            }
            if capture.dropped_events > 0 {
                ui.label(format!(
                    "Diagnostic capture dropped {} blocks",
                    capture.dropped_events
                ));
            }
        }
    }
}
