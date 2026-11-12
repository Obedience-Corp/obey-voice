//! Model storage: where the models live, how big they are, and moving them.
//! The engine's `models usage` and `models move` own every file operation.
use crate::app::VoiceApp;
use crate::model::Purpose;
use crate::screens::{card, field};
use eframe::egui;
use serde_json::Value;

pub(crate) const MOVE_ACTION: &str = "@models_move";

pub(crate) fn move_arguments(destination: &str) -> Vec<String> {
    ["models", "move", "--", destination.trim()]
        .map(str::to_owned)
        .to_vec()
}

fn plan_arguments(destination: &str) -> Vec<String> {
    [
        "models",
        "move",
        "--dry-run",
        "--json",
        "--",
        destination.trim(),
    ]
    .map(str::to_owned)
    .to_vec()
}

fn usage_arguments() -> Vec<String> {
    ["models", "usage"].map(str::to_owned).to_vec()
}

/// The dry run's plan, which a refused check also prints before failing.
fn plan_from(result: Result<Value, String>) -> Result<Value, String> {
    match result {
        Ok(plan) if plan["type"] == "plan" => Ok(plan),
        Ok(other) => Err(format!("Unexpected answer from the move check: {other}")),
        Err(text) => text
            .lines()
            .filter_map(|line| serde_json::from_str::<Value>(line).ok())
            .find(|v| v["type"] == "plan")
            .ok_or(text),
    }
}

fn bytes(n: u64) -> String {
    const UNITS: [&str; 6] = ["KiB", "MiB", "GiB", "TiB", "PiB", "EiB"];
    if n < 1024 {
        return format!("{n} B");
    }
    let (mut value, mut unit) = (n as f64 / 1024.0, 0);
    while value >= 1024.0 && unit < UNITS.len() - 1 {
        value /= 1024.0;
        unit += 1;
    }
    format!("{value:.1} {}", UNITS[unit])
}

/// What the confirmation says about a checked move, or why it cannot run.
pub(crate) fn confirm_text(plan: &Value) -> Result<String, String> {
    let refusals: Vec<&str> = plan["refusals"]
        .as_array()
        .map(|r| r.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    if !refusals.is_empty() {
        return Err(format!("Models can't be moved: {}", refusals.join("; ")));
    }
    let mut text = format!(
        "Move {} in {} files from {} to {}?",
        bytes(plan["bytes"].as_u64().unwrap_or(0)),
        plan["files"].as_u64().unwrap_or(0),
        plan["from"].as_str().unwrap_or_default(),
        plan["to"].as_str().unwrap_or_default(),
    );
    text.push_str(if plan["same_volume"].as_bool().unwrap_or(false) {
        "\n\nIt is renamed in place on the same drive."
    } else {
        "\n\nIt is copied to the other drive and checked; the old folder is removed after."
    });
    if let Some(entries) = plan["unrecognized"].as_array().filter(|e| !e.is_empty()) {
        text.push_str("\n\nNot used by your settings or personas (moved too, and the old folder is then kept, not deleted):");
        for entry in entries.iter().take(5) {
            text.push_str(&format!(
                "\n  {}  {}",
                entry["name"].as_str().unwrap_or_default(),
                bytes(entry["bytes"].as_u64().unwrap_or(0))
            ));
        }
        if entries.len() > 5 {
            text.push_str(&format!("\n  and {} more", entries.len() - 5));
        }
    }
    text.push_str("\n\nCancel works until the new folder is saved; after that the move finishes.");
    if cfg!(windows) {
        text.push_str(" On Windows, Cancel stops the move at once without cleaning up.");
    }
    Ok(text)
}

impl VoiceApp {
    pub(crate) fn complete_move_plan(
        &mut self,
        destination: String,
        result: Result<Value, String>,
    ) {
        match plan_from(result).and_then(|plan| confirm_text(&plan)) {
            Ok(text) => self.confirm = Some((text, vec![MOVE_ACTION.into(), destination])),
            Err(err) => self.error = Some(err),
        }
    }

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
                    ui.label("Move the models to an empty or new folder, given as a full path, such as one on a larger drive. The move is checked first and you confirm what it will do. Cancelling before the new folder is saved keeps the current folder in use; the old folder is removed only after every file in it has been read and matched.");
                    field(ui, "Move models to", &mut self.models_move_to);
                    let destination = self.models_move_to.trim().to_owned();
                    if ui
                        .add_enabled(
                            !destination.is_empty() && !from_environment,
                            egui::Button::new("Move models…"),
                        )
                        .clicked()
                    {
                        self.cli(
                            plan_arguments(&destination),
                            Purpose::MovePlan(destination),
                            false,
                        );
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
    use serde_json::json;

    #[test]
    fn move_passes_the_trimmed_destination_after_a_separator() {
        assert_eq!(
            move_arguments("  /Volumes/Big Drive/models "),
            ["models", "move", "--", "/Volumes/Big Drive/models"]
        );
        assert_eq!(
            plan_arguments("/x"),
            ["models", "move", "--dry-run", "--json", "--", "/x"]
        );
        assert_eq!(usage_arguments(), ["models", "usage"]);
    }

    #[test]
    fn confirmation_names_size_and_what_keeps_the_old_folder() {
        let plan = json!({"type":"plan","from":"/old","to":"/fast","bytes": 3u64 << 30,"files":412,
            "same_volume":false,"unrecognized":[{"name":"lmstudio","bytes":1024}]});
        let text = confirm_text(&plan).unwrap();
        for want in [
            "Move 3.0 GiB in 412 files from /old to /fast?",
            "copied to the other drive",
            "lmstudio  1.0 KiB",
            "old folder is then kept",
            "Cancel works until the new folder is saved",
        ] {
            assert!(text.contains(want), "{want:?} missing from {text}");
        }
    }

    #[test]
    fn a_refused_check_reads_the_plan_from_the_failure_output() {
        let failed =
            Err("{\"type\":\"plan\",\"refusals\":[\"/fast already has files in it\"]}".to_string());
        let plan = plan_from(failed).unwrap();
        assert_eq!(
            confirm_text(&plan).unwrap_err(),
            "Models can't be moved: /fast already has files in it"
        );
        assert_eq!(plan_from(Err("boom".into())).unwrap_err(), "boom");
    }

    #[test]
    fn only_a_real_move_is_left_to_finish_its_cleanup() {
        use crate::backend::finishes_on_its_own;
        assert!(finishes_on_its_own(&move_arguments("/x")));
        assert!(!finishes_on_its_own(&plan_arguments("/x")));
        assert!(!finishes_on_its_own(&usage_arguments()));
    }
}
