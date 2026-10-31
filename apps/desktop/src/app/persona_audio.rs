use crate::app::VoiceApp;
use crate::model::Purpose;
use serde_json::Value;

impl VoiceApp {
    pub(crate) fn stop_persona_audio_check(&mut self) {
        if let Some(id) = self.persona_form.audio_check.take() {
            self.pending.remove(&id);
            self.backend.cancel(id);
            self.persona_form.audio_notice = "Setup check cancelled.".into();
        }
    }

    pub(crate) fn start_persona_audio_check(&mut self) {
        self.stop_persona_audio_check();
        self.stop_persona_preview();
        let form = &self.persona_form;
        if form.id.is_empty() || self.demo {
            return;
        }
        let mut args = vec![
            "persona".into(),
            "check-audio".into(),
            form.id.clone(),
            "--json".into(),
        ];
        form.audio.append_flags(&mut args);
        let draft = form.audio.clone();
        let id = self.cli(args, Purpose::PersonaAudioCheck, false);
        self.persona_form.audio_check = Some(id);
        self.persona_form.audio_checked_draft = Some(draft);
        self.persona_form.audio_notice = "Checking local runtime quietly…".into();
        self.persona_form.audio_error = false;
    }

    pub(super) fn complete_persona_audio_check(&mut self, id: u64, result: Result<Value, String>) {
        let form = &mut self.persona_form;
        if form.audio_check != Some(id) {
            return;
        }
        form.audio_check = None;
        if !form.open || form.audio_checked_draft.as_ref() != Some(&form.audio) {
            form.audio_notice.clear();
            return;
        }
        match result {
            Ok(value) if value["protocol_ready"].as_bool() == Some(true) => {
                form.audio_notice = "Setup passed. Experimental responses can be inaccurate or claim actions that did not occur.".into();
                form.audio_error = false;
            }
            Ok(_) => {
                form.audio_notice = "Runtime did not confirm native audio support.".into();
                form.audio_error = true;
            }
            Err(error) => {
                form.audio_notice = error;
                form.audio_error = true;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn late_setup_cannot_publish_after_edit_cancel_or_dialog_close() {
        if std::env::var_os("OBEY_AUDIO_SETUP_TEST").is_none() {
            assert!(std::process::Command::new(std::env::current_exe().unwrap())
                .args(["--exact", "app::persona_audio::tests::late_setup_cannot_publish_after_edit_cancel_or_dialog_close"])
                .env("OBEY_AUDIO_SETUP_TEST", "1")
                .env("OBEY_VOICE_AGENT_DEMO", "1")
                .status().unwrap().success());
            return;
        }
        let cc = eframe::CreationContext::_new_kittest(Default::default());
        let mut app = VoiceApp::new(&cc);
        app.persona_form.open = true;
        app.persona_form.audio.mode = "native".into();
        app.persona_form.audio_check = Some(10);
        app.persona_form.audio_checked_draft = Some(app.persona_form.audio.clone());
        app.complete_persona_audio_check(9, Ok(json!({"protocol_ready":true})));
        assert_eq!(app.persona_form.audio_check, Some(10));
        app.persona_form.audio.voice = "changed".into();
        app.complete_persona_audio_check(10, Ok(json!({"protocol_ready":true})));
        assert!(app.persona_form.audio_notice.is_empty());
        app.persona_form.audio_check = Some(11);
        app.stop_persona_audio_check();
        app.complete_persona_audio_check(11, Ok(json!({"protocol_ready":true})));
        assert_eq!(app.persona_form.audio_notice, "Setup check cancelled.");
        app.persona_form.audio_check = Some(12);
        app.persona_form.audio_checked_draft = Some(app.persona_form.audio.clone());
        app.persona_form.open = false;
        app.complete_persona_audio_check(12, Ok(json!({"protocol_ready":true})));
        assert!(app.persona_form.audio_notice.is_empty());
    }
}
