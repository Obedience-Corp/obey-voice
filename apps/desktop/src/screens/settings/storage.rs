//! Model storage: where the models live, how big they are, and moving them.
//! The engine's `models usage` and `models move` own every file operation.
use crate::app::VoiceApp;
use crate::screens::{card, field};
use eframe::egui;

pub(crate) const MOVE_ACTION: &str = "@models_move";

pub(crate) fn move_arguments(destination: &str) -> Vec<String> {
    ["models", "move", destination.trim()]
        .map(str::to_owned)
        .to_vec()
}

fn usage_arguments() -> Vec<String> {
    ["models", "usage"].map(str::to_owned).to_vec()
}

impl VoiceApp {
    pub(super) fn model_storage(&mut self, ui: &mut egui::Ui) {
        ui.push_id("model_storage", |ui| {
            card(ui, |ui| {
                ui.heading("Model storage");
                let folder = self.values["models_dir"]["value"]
                    .as_str()
                    .unwrap_or_default()
                    .to_owned();
                ui.label("Every speech, voice and decision model is stored in one folder:");
                ui.monospace(&folder);
                let from_environment = self.values["models_dir"]["source"] == "env";
                if from_environment {
                    ui.label("This folder is set by the MODELS_DIR environment variable, so it cannot be moved from here.");
                }
                let idle = !self.active && !self.starting && self.task.is_none();
                ui.add_enabled_ui(idle, |ui| {
                    if ui.button("Measure size").clicked() {
                        self.start_task("Model storage", usage_arguments());
                    }
                    ui.separator();
                    ui.label("Move the models to an empty or new folder, such as one on a larger drive. The current folder is removed only after the new one is complete; cancelling keeps the current folder in use.");
                    field(ui, "Move models to", &mut self.models_move_to);
                    let destination = self.models_move_to.trim().to_owned();
                    if ui
                        .add_enabled(
                            !destination.is_empty() && !from_environment,
                            egui::Button::new("Move models…"),
                        )
                        .clicked()
                    {
                        self.confirm = Some((
                            format!("Move every model from {folder} to {destination}?"),
                            vec![MOVE_ACTION.into(), destination],
                        ));
                    }
                });
                if self.active || self.starting {
                    ui.label("Stop the conversation before moving models; it is reading these files.");
                }
                ui.label("To use models that are already in another folder, edit Models folder below instead. That changes the setting and moves nothing.");
            });
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn move_passes_the_trimmed_destination_as_one_argument() {
        assert_eq!(
            move_arguments("  /Volumes/Big Drive/models "),
            ["models", "move", "/Volumes/Big Drive/models"]
        );
        assert_eq!(usage_arguments(), ["models", "usage"]);
    }
}
