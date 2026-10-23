use crate::app::VoiceApp;
use crate::model::Screen;
use eframe::egui;
use serde_json::Value;

#[derive(Default, Debug, PartialEq, Eq)]
struct WorkCue {
    active: usize,
    queued: usize,
    single_id: Option<String>,
}

impl WorkCue {
    fn from_workers(workers: &[Value]) -> Self {
        let mut cue = Self::default();
        for worker in workers {
            // Matches the engine's WorkerInfo.Active projection; stopping work
            // remains visible until the engine confirms termination.
            match worker["State"].as_str() {
                Some("queued") => cue.queued += 1,
                Some("running" | "stopping") => cue.active += 1,
                _ => continue,
            }
            cue.single_id = if cue.active + cue.queued == 1 {
                worker["ID"].as_str().map(str::to_owned)
            } else {
                None
            };
        }
        cue
    }
}

impl VoiceApp {
    pub(super) fn work_cue(&mut self, ui: &mut egui::Ui) {
        let cue = WorkCue::from_workers(&self.state.workers);
        if cue.active + cue.queued == 0 {
            return;
        }
        ui.horizontal_wrapped(|ui| {
            ui.spinner();
            if ui
                .button(format!(
                    "Background work · {} active · {} queued",
                    cue.active, cue.queued
                ))
                .on_hover_text("Inspect background tasks")
                .clicked()
            {
                self.selected_worker = cue.single_id;
                self.navigate(Screen::Workers);
            }
            if !self.state.workers_enabled {
                ui.small("New tasks disabled");
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn cue_tracks_active_snapshot_and_ignores_completed_or_unknown_jobs() {
        let mut workers = vec![
            json!({"ID":"one", "State":"running"}),
            json!({"ID":"two", "State":"queued"}),
            json!({"ID":"three", "State":"stopping"}),
            json!({"ID":"old", "State":"succeeded"}),
            json!({"ID":"bad", "State":"failed"}),
            json!({"ID":"unknown"}),
        ];
        assert_eq!(
            WorkCue::from_workers(&workers),
            WorkCue {
                active: 2,
                queued: 1,
                single_id: None
            }
        );
        workers[0]["State"] = json!("succeeded");
        workers[2]["State"] = json!("canceled");
        assert_eq!(
            WorkCue::from_workers(&workers),
            WorkCue {
                active: 0,
                queued: 1,
                single_id: Some("two".into())
            }
        );
        workers[1]["State"] = json!("failed");
        assert_eq!(WorkCue::from_workers(&workers), WorkCue::default());
    }
}
