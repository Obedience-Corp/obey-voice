use super::VoiceApp;
use crate::model::Purpose;
use eframe::egui;
use serde_json::{Value, json};

impl VoiceApp {
    fn ollama_host_draft_unsaved(&self) -> bool {
        self.drafts.get("ollama_host").is_some_and(|draft| {
            draft
                != self.values["ollama_host"]["value"]
                    .as_str()
                    .unwrap_or_default()
        })
    }
    fn ollama_key(&self) -> String {
        format!(
            "{}\n{}",
            self.values["brain_provider"]["value"]
                .as_str()
                .unwrap_or("ollama"),
            self.values["ollama_host"]["value"]
                .as_str()
                .unwrap_or_default()
        )
    }
    pub(crate) fn refresh_ollama_catalog(&mut self) {
        let key = self.ollama_key();
        let host = key
            .split_once('\n')
            .map(|(_, host)| host)
            .unwrap_or_default();
        let id = self.request(
            "ollama_models",
            json!({"config":{"host":host}}),
            Purpose::OllamaCatalog(key.clone()),
        );
        self.ollama_catalog_request = Some((id, key));
        self.ollama_catalog = Value::Null;
        self.ollama_catalog_key = None;
    }
    pub(super) fn ollama_catalog_result(
        &mut self,
        id: u64,
        key: String,
        result: Result<Value, String>,
    ) {
        if self.ollama_catalog_request.as_ref() != Some(&(id, key.clone())) {
            return;
        }
        self.ollama_catalog_request = None;
        if key != self.ollama_key()
            || self.values["brain_provider"]["value"] != "ollama"
            || self.ollama_host_draft_unsaved()
        {
            self.invalidate_ollama_catalog();
            return;
        }
        self.ollama_catalog = result.unwrap_or_else(|error| json!({"problem":error}));
        self.ollama_catalog_key = Some(key);
    }
    pub(crate) fn ollama_model_picker(&mut self, ui: &mut egui::Ui) {
        let key = self.ollama_key();
        if self
            .ollama_catalog_request
            .as_ref()
            .is_some_and(|(_, request_key)| request_key != &key)
            || self
                .ollama_catalog_key
                .as_ref()
                .is_some_and(|catalog_key| catalog_key != &key)
        {
            self.invalidate_ollama_catalog();
        }
        if self.values["brain_provider"]["value"] != "ollama" {
            self.invalidate_ollama_catalog();
            return;
        }
        if self.ollama_host_draft_unsaved() {
            self.invalidate_ollama_catalog();
            ui.label("Save the Ollama server address before choosing a model.");
            return;
        }
        if self.ollama_catalog.is_null() && self.ollama_catalog_request.is_none() && !self.demo {
            self.refresh_ollama_catalog();
        }
        ui.horizontal(|ui| {
            let loading = self.ollama_catalog_request.is_some();
            if ui
                .add_enabled(
                    !loading,
                    egui::Button::new(if loading {
                        "Loading Ollama models…"
                    } else {
                        "Refresh available Ollama models"
                    }),
                )
                .clicked()
            {
                self.refresh_ollama_catalog();
            }
        });
        let saved = self.values["ollama_model"]["value"]
            .as_str()
            .unwrap_or_default()
            .to_owned();
        let catalog_loaded = self.ollama_catalog.as_array().is_some();
        let available = self.ollama_catalog.as_array().is_some_and(|models| {
            models
                .iter()
                .any(|model| model.as_str() == Some(saved.as_str()))
        });
        if !saved.is_empty() && !available {
            if catalog_loaded {
                ui.label(format!(
                    "Saved model: {saved} (not available on this server)"
                ));
            } else {
                ui.label(format!("Saved model: {saved}"));
            }
        }
        if let Some(problem) = self.ollama_catalog["problem"].as_str() {
            ui.label(format!("Could not load Ollama models: {problem}"));
        }
        if let Some(models) = self.ollama_catalog.as_array().cloned() {
            if models.is_empty() {
                ui.label("No models are installed on this Ollama server.");
            }
            egui::ComboBox::from_id_salt("ollama_model_catalog")
                .selected_text(if saved.is_empty() {
                    "Choose an installed model"
                } else {
                    &saved
                })
                .show_ui(ui, |ui| {
                    for model in models.iter().filter_map(Value::as_str) {
                        if ui.selectable_label(saved == model, model).clicked() {
                            self.cli(
                                vec![
                                    "config".into(),
                                    "set".into(),
                                    "ollama_model".into(),
                                    model.into(),
                                    "--json".into(),
                                ],
                                Purpose::SettingSave("ollama_model".into()),
                                false,
                            );
                        }
                    }
                });
        } else if self.ollama_catalog_request.is_none() && self.ollama_catalog["problem"].is_null()
        {
            ui.small("Refresh to load models available from this Ollama server.");
        }
    }
    pub(super) fn invalidate_ollama_catalog(&mut self) {
        self.ollama_catalog = Value::Null;
        self.ollama_catalog_key = None;
        self.ollama_catalog_request = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::Screen;

    #[test]
    fn catalog_states_preserve_saved_unavailable_model_and_ignore_stale_hosts() {
        if std::env::var_os("VOICE_OLLAMA_CATALOG_TEST").is_none() {
            assert!(std::process::Command::new(std::env::current_exe().unwrap())
                .args(["--exact", "app::ollama_catalog::tests::catalog_states_preserve_saved_unavailable_model_and_ignore_stale_hosts"])
                .env("VOICE_OLLAMA_CATALOG_TEST", "1")
                .env("OBEY_VOICE_AGENT_DEMO", "1")
                .status().unwrap().success());
            return;
        }
        let cc = eframe::CreationContext::_new_kittest(Default::default());
        let mut app = VoiceApp::new(&cc);
        app.screen = Screen::Settings;
        app.values = json!({"brain_provider":{"value":"ollama"},"ollama_host":{"value":"http://old"},"ollama_model":{"value":"removed-model"}});
        app.ollama_catalog_request = Some((7, "ollama\nhttp://old".into()));
        app.ollama_catalog_result(
            7,
            "ollama\nhttp://old".into(),
            Ok(json!(["available-model"])),
        );
        assert_eq!(app.ollama_catalog, json!(["available-model"]));
        assert_eq!(app.values["ollama_model"]["value"], "removed-model");

        app.drafts.insert("ollama_host".into(), "http://new".into());
        assert!(app.ollama_host_draft_unsaved());
        app.invalidate_ollama_catalog();
        assert!(app.ollama_catalog.is_null());

        app.drafts.remove("ollama_host");
        app.ollama_catalog_request = Some((8, "ollama\nhttp://old".into()));
        app.drafts.insert("ollama_host".into(), "http://new".into());
        app.ollama_catalog_result(8, "ollama\nhttp://old".into(), Ok(json!(["stale"])));
        assert!(app.ollama_catalog.is_null());
        assert_eq!(app.ollama_catalog_request, None);

        app.drafts.remove("ollama_host");
        app.ollama_catalog_request = Some((9, app.ollama_key()));
        app.ollama_catalog_result(9, app.ollama_key(), Err("server unavailable".into()));
        assert_eq!(app.ollama_catalog["problem"], "server unavailable");
        app.ollama_catalog_request = Some((10, app.ollama_key()));
        app.ollama_catalog_result(10, app.ollama_key(), Ok(json!([])));
        assert_eq!(app.ollama_catalog, json!([]));
    }
}
