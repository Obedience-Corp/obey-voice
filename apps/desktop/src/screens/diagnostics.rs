use crate::app::VoiceApp;
use eframe::egui;
use eframe::egui::{Color32, RichText};

impl VoiceApp {
    pub fn diagnostics_dialog(&mut self, ctx: &egui::Context) {
        if !self.diagnostics_open {
            return;
        }
        let snapshot = crate::diagnostics::snapshot();
        let modal = egui::Modal::new(egui::Id::unique("diagnostics"))
            .frame(crate::theme::dialog(self.dark))
            .show(ctx, |ui| {
                ui.set_width((ctx.content_rect().width() - 100.).clamp(300., 820.));
                ui.heading("Diagnostics");
                ui.label("Desktop events, engine output, and command failures are saved locally.");
                ui.add(
                    egui::Label::new(RichText::new(snapshot.path.display().to_string()).small())
                        .selectable(true),
                );
                if let Some(error) = &snapshot.error {
                    ui.colored_label(Color32::LIGHT_RED, format!("Log file unavailable: {error}"));
                }
                ui.horizontal(|ui| {
                    if ui.button("Copy log path").clicked() {
                        ctx.copy_text(snapshot.path.display().to_string());
                    }
                    if ui.button("Copy visible logs").clicked() {
                        ctx.copy_text(
                            snapshot
                                .lines
                                .iter()
                                .filter(|l| l.contains(&self.diagnostics_filter))
                                .cloned()
                                .collect::<Vec<_>>()
                                .join("\n"),
                        );
                    }
                    if ui.button("Close").clicked() {
                        self.diagnostics_open = false;
                    }
                });
                ui.add(
                    egui::TextEdit::singleline(&mut self.diagnostics_filter)
                        .hint_text("Filter by error, event, or request ID…")
                        .desired_width(f32::INFINITY),
                );
                egui::ScrollArea::vertical()
                    .max_height((ctx.content_rect().height() - 300.).max(150.))
                    .stick_to_bottom(true)
                    .show(ui, |ui| {
                        for line in &snapshot.lines {
                            if line.contains(&self.diagnostics_filter) {
                                ui.add(
                                    egui::Label::new(RichText::new(line).monospace().size(11.))
                                        .wrap()
                                        .selectable(true),
                                );
                            }
                        }
                    });
            });
        if modal.should_close() {
            self.diagnostics_open = false;
        }
        ctx.request_repaint_after(std::time::Duration::from_millis(250));
    }
}
