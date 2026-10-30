//! Explicit decision-model setup uses the engine's shared verified installer.
use crate::app::VoiceApp;
use crate::screens::card;
use eframe::egui;

pub(super) fn arguments(install: bool) -> Vec<String> {
    [
        "models",
        "decision",
        if install { "install" } else { "status" },
    ]
    .map(str::to_owned)
    .to_vec()
}

impl VoiceApp {
    pub(super) fn decision_model_setup(&mut self, ui: &mut egui::Ui) {
        ui.push_id("decision_model_setup", |ui| {
            card(ui, |ui| {
                ui.heading("Local decision model setup");
                ui.label("Install before enabling decision shortcuts. Setup does not enable routing or change your provider.");
                ui.label("Save the model, compute device, and storage choices below before installing. Choose a fast drive or an existing Obey Voice model root to reuse verified files.");
                ui.label("Storage is shared with speech models. Changing the directory does not move existing files. Runtime dependencies need additional disk space beyond the model download.");
                ui.add_enabled_ui(!self.active && !self.starting && self.task.is_none(), |ui| {
                    let specs: Vec<_> = self.schema.iter().filter(|s| matches!(s.key.as_str(), "decision_local_model" | "decision_device" | "models_dir")).cloned().collect();
                    for spec in specs {
                        self.setup_setting_editor(ui, &spec);
                    }
                    ui.horizontal_wrapped(|ui| {
                        if ui.button("Check installed model / verify").clicked() {
                            self.start_task("Decision model status and requirements", arguments(false));
                        }
                        if ui.button("Install / repair selected model").clicked() {
                            self.start_task("Decision model setup", arguments(true));
                        }
                    });
                });
                if self.active || self.starting {
                    ui.label("Stop the conversation before model setup.");
                }
                ui.label("Check shows download size, memory guidance, license, location, and verified installation state. Install reuses verified files and repairs missing or corrupt files. Progress and Cancel appear below; use Install / repair again after an error or cancellation.");
                ui.hyperlink_to("Liquid d1 license and commercial-use terms", "https://huggingface.co/LiquidAI/d1-3B/blob/main/LICENSE");
            });
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn actions_are_scoped_and_do_not_enable_routing() {
        assert_eq!(arguments(false), ["models", "decision", "status"]);
        assert_eq!(arguments(true), ["models", "decision", "install"]);
    }
}
