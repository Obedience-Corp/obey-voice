use super::VoiceApp;
use crate::model::{Purpose, string};
use serde_json::Value;

impl VoiceApp {
    pub fn load_persona_directories(&mut self) {
        self.persona_form.directory_notice.clear();
        let id = self.cli(
            vec!["persona".into(), "directories".into(), "--json".into()],
            Purpose::PersonaDirectories,
            false,
        );
        self.persona_form.directory_request = Some(id);
    }

    pub fn browse_persona_directory(&mut self) {
        let id = self.next_id;
        self.next_id += 1;
        if self.demo {
            return;
        }
        self.pending.insert(id, Purpose::PersonaBrowse);
        self.persona_form.browse_request = Some(id);
        self.backend.pick_directory(id);
    }

    pub(super) fn complete_persona_directory(
        &mut self,
        id: u64,
        purpose: Purpose,
        result: Result<Value, String>,
    ) {
        let f = &mut self.persona_form;
        if !f.open {
            return;
        }
        let browse = matches!(purpose, Purpose::PersonaBrowse);
        let pending = if browse {
            &mut f.browse_request
        } else {
            &mut f.directory_request
        };
        if *pending != Some(id) {
            return;
        }
        *pending = None;
        match result {
            Err(err) => f.directory_notice = err,
            Ok(value) if browse => {
                if let Some(path) = value.as_str() {
                    f.starting_directory = path.into();
                }
            }
            Ok(value) => {
                f.directory_choices = value["campaigns"].as_array().cloned().unwrap_or_default();
                f.directory_notice = string(&value, "notice");
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use crate::model::{PersonaForm, Purpose};
    use serde_json::json;

    #[test]
    fn discovery_failure_preserves_custom_path_and_stale_browse_is_ignored() {
        if std::env::var_os("OBEY_DIRECTORY_TEST").is_none() {
            assert!(std::process::Command::new(std::env::current_exe().unwrap())
                .args(["--exact", "app::persona_directory::tests::discovery_failure_preserves_custom_path_and_stale_browse_is_ignored"])
                .env("OBEY_DIRECTORY_TEST", "1").env("OBEY_VOICE_AGENT_DEMO", "1")
                .status().unwrap().success());
            return;
        }
        let cc = eframe::CreationContext::_new_kittest(Default::default());
        let mut app = super::VoiceApp::new(&cc);
        app.persona_form = PersonaForm {
            open: true,
            starting_directory: "/work/custom".into(),
            directory_request: Some(7),
            browse_request: Some(9),
            ..Default::default()
        };
        app.complete_persona_directory(
            7,
            Purpose::PersonaDirectories,
            Err("camp unavailable".into()),
        );
        assert_eq!(app.persona_form.starting_directory, "/work/custom");
        assert_eq!(app.persona_form.directory_notice, "camp unavailable");
        app.complete_persona_directory(8, Purpose::PersonaBrowse, Ok(json!("/stale")));
        assert_eq!(app.persona_form.starting_directory, "/work/custom");
        app.complete_persona_directory(9, Purpose::PersonaBrowse, Ok(json!("/work/chosen")));
        assert_eq!(app.persona_form.starting_directory, "/work/chosen");
    }
}
