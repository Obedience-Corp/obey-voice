use super::VoiceApp;
use crate::model::Purpose;
use eframe::egui;
use serde_json::{Value, json};

const KEYS: [&str; 4] = [
    "obey_socket",
    "obey_provider",
    "obey_model",
    "obey_tool_policy",
];

impl VoiceApp {
    pub(super) fn obey_route_key(&self) -> String {
        let values: Vec<_> = KEYS
            .iter()
            .map(|key| self.values[*key]["value"].clone())
            .collect();
        serde_json::to_string(&values).unwrap_or_default()
    }
    pub(super) fn load_obey_catalog(&mut self, refresh: bool) {
        let key = self.obey_route_key();
        let mut config = json!({"refresh":refresh});
        for name in KEYS {
            config[name] = self.values[name]["value"]
                .as_str()
                .unwrap_or_default()
                .into();
        }
        let id = self.request(
            "obey_catalog",
            json!({"config":config}),
            Purpose::ObeyCatalog(key),
        );
        self.obey_catalog_request = Some(id);
    }
    pub(super) fn complete_obey_catalog(
        &mut self,
        id: u64,
        key: String,
        result: Result<Value, String>,
    ) {
        if self.obey_catalog_request != Some(id) {
            return;
        }
        self.obey_catalog_request = None;
        if key != self.obey_route_key() || self.values["brain_provider"]["value"] != "obey" {
            return;
        }
        self.obey_catalog = result.unwrap_or_else(|err| json!({"problem":err}));
    }
    pub(crate) fn obey_catalog_ui(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            if ui
                .add_enabled(
                    self.obey_catalog_request.is_none(),
                    egui::Button::new("Load daemon catalog"),
                )
                .clicked()
            {
                self.load_obey_catalog(false);
            }
            if ui
                .add_enabled(
                    self.obey_catalog_request.is_none()
                        && self.values["obey_provider"]["value"]
                            .as_str()
                            .is_some_and(|s| !s.is_empty()),
                    egui::Button::new("Refresh daemon models"),
                )
                .clicked()
            {
                self.load_obey_catalog(true);
            }
        });
        let catalog = self.obey_catalog.clone();
        if let Some(problem) = catalog["problem"].as_str() {
            ui.label(problem);
        }
        for (field, rows) in [
            ("obey_provider", &catalog["providers"]["providers"]),
            ("obey_model", &catalog["models"]["models"]),
        ] {
            let Some(rows) = rows.as_array() else {
                continue;
            };
            egui::ComboBox::from_id_salt(("daemon_catalog", field))
                .selected_text(if field == "obey_provider" {
                    "Choose daemon provider"
                } else {
                    "Choose daemon model"
                })
                .show_ui(ui, |ui| {
                    for row in rows {
                        let Some(id) = row["id"].as_str() else {
                            continue;
                        };
                        if ui
                            .selectable_label(self.values[field]["value"] == id, id)
                            .clicked()
                        {
                            self.cli(
                                vec![
                                    "config".into(),
                                    "set".into(),
                                    field.into(),
                                    id.into(),
                                    "--json".into(),
                                ],
                                Purpose::SettingSave(field.into()),
                                false,
                            );
                        }
                    }
                });
        }
        if let Some(state) = catalog["models"]["catalog_state"].as_str() {
            ui.small(format!("Catalog: {state}. Manual IDs remain available."));
        }
        if let Some(checks) = catalog["readiness"]["checks"].as_array() {
            for check in checks {
                ui.label(format!(
                    "{}: {} · {}",
                    check["stage"].as_str().unwrap_or_default(),
                    check["state"].as_str().unwrap_or_default(),
                    check["code"].as_str().unwrap_or_default()
                ));
                if let Some(remedy) = check["remediation"].as_str() {
                    ui.small(remedy);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use crate::app::VoiceApp;
    use crate::backend::Event;
    use crate::model::{Purpose, Screen};
    use serde_json::json;
    use std::sync::mpsc;

    #[test]
    fn stale_catalog_cannot_replace_selected_daemon_or_native_provider() {
        if std::env::var_os("VOICE_OBEY_CATALOG_TEST").is_none() {
            assert!(std::process::Command::new(std::env::current_exe().unwrap())
                .args(["--exact", "app::obey_catalog::tests::stale_catalog_cannot_replace_selected_daemon_or_native_provider"])
                .env("VOICE_OBEY_CATALOG_TEST", "1")
                .env("OBEY_VOICE_AGENT_DEMO", "1")
                .status().unwrap().success());
            return;
        }
        let cc = eframe::CreationContext::_new_kittest(Default::default());
        let mut app = VoiceApp::new(&cc);
        app.screen = Screen::Settings;
        app.values = json!({"brain_provider":{"value":"obey"}, "obey_socket":{"value":"old.sock"}, "obey_provider":{"value":"saved-profile"}, "obey_model":{"value":"manual-model"}, "obey_tool_policy":{"value":"strict"}});
        let old_key = app.obey_route_key();
        app.values["obey_socket"]["value"] = json!("selected.sock");
        let selected_key = app.obey_route_key();
        app.pending.insert(41, Purpose::ObeyCatalog(old_key));
        app.pending.insert(42, Purpose::ObeyCatalog(selected_key));
        app.obey_catalog_request = Some(42);
        let (tx, events) = mpsc::channel();
        app.backend.events = events;
        tx.send(Event::Result {
            id: 41,
            result: Ok(json!({"provider":"foreign-profile"})),
        })
        .unwrap();
        app.poll();
        assert!(app.obey_catalog.is_null());
        assert_eq!(app.obey_catalog_request, Some(42));
        tx.send(Event::Result {
            id: 42,
            result: Ok(json!({"provider":"saved-profile"})),
        })
        .unwrap();
        app.poll();
        assert_eq!(app.obey_catalog["provider"], "saved-profile");
        assert_eq!(app.values["obey_model"]["value"], "manual-model");
        let key = app.obey_route_key();
        app.pending.insert(43, Purpose::ObeyCatalog(key));
        app.obey_catalog_request = Some(43);
        app.obey_catalog = serde_json::Value::Null;
        app.values["brain_provider"]["value"] = json!("ollama");
        tx.send(Event::Result {
            id: 43,
            result: Ok(json!({"provider":"saved-profile"})),
        })
        .unwrap();
        app.poll();
        assert!(app.obey_catalog.is_null());
        assert_eq!(app.values["brain_provider"]["value"], "ollama");
    }
}
