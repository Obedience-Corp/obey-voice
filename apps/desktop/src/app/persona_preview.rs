use crate::app::VoiceApp;
use crate::model::{Purpose, string};
use serde_json::{Value, json};

impl VoiceApp {
    pub(super) fn start_persona_preview(&mut self) {
        self.stop_persona_preview();
        if self.demo {
            self.persona_form.preview_notice = "Voice playback is unavailable in demo mode".into();
            return;
        }
        self.persona_form.preview_persona.clear();
        let f = &self.persona_form;
        let args =
            json!({"voice_selection": {"provider": f.tts, "voice": f.voice, "tier": f.tier}});
        let id = self.request("persona_preview", args, Purpose::PersonaPreview);
        self.persona_form.preview_request = Some(id);
        self.persona_form.preview_notice = "Preparing and playing preview…".into();
        self.persona_form.preview_error = false;
    }

    pub(crate) fn preview_saved_persona(&mut self, persona_id: &str) {
        if self.persona_form.preview_persona == persona_id
            && self.persona_form.preview_request.is_some()
        {
            self.stop_persona_preview();
            return;
        }
        self.stop_persona_preview();
        if self.demo || !self.connected {
            return;
        }
        let id = self.request(
            "persona_preview",
            json!({"voice_selection": {"persona_id": persona_id}}),
            Purpose::PersonaPreview,
        );
        self.persona_form.preview_persona = persona_id.into();
        self.persona_form.preview_request = Some(id);
        self.persona_form.preview_notice = "Preparing and playing preview…".into();
        self.persona_form.preview_error = false;
    }

    pub(crate) fn toggle_persona_preview(&mut self) {
        if self.persona_form.preview_request.is_some() {
            self.stop_persona_preview();
        } else {
            self.start_persona_preview();
        }
    }

    pub(crate) fn stop_persona_preview(&mut self) {
        if let Some(id) = self.persona_form.preview_request.take() {
            self.pending.remove(&id);
            self.request("persona_preview_stop", json!({}), Purpose::Ignore);
            self.persona_form.preview_notice = "Preview stopped".into();
            self.persona_form.preview_error = false;
        }
    }

    pub(super) fn complete_persona_preview(&mut self, id: u64, result: Result<Value, String>) {
        if self.persona_form.preview_request == Some(id)
            && let Err(error) = result
        {
            self.persona_form.preview_request = None;
            self.persona_form.preview_notice = error;
            self.persona_form.preview_error = true;
        }
    }

    pub(super) fn persona_preview_done(&mut self, event: &Value) {
        let f = &mut self.persona_form;
        if f.preview_request.is_some() && f.preview_request == event["id"].as_u64() {
            f.preview_request = None;
            f.preview_notice = string(event, "message");
            f.preview_error = event["error"].as_bool().unwrap_or(false);
        }
    }

    pub(super) fn persona_preview_lost(&mut self, error: &str) {
        if let Some(id) = self.persona_form.preview_request.take() {
            self.pending.remove(&id);
            self.persona_form.preview_notice = error.into();
            self.persona_form.preview_error = true;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn preview_completion_stop_and_disconnect_preserve_draft() {
        if std::env::var_os("OBEY_PREVIEW_TEST").is_none() {
            assert!(std::process::Command::new(std::env::current_exe().unwrap())
                .args(["--exact", "app::persona_preview::tests::preview_completion_stop_and_disconnect_preserve_draft"])
                .env("OBEY_PREVIEW_TEST", "1").env("OBEY_VOICE_AGENT_DEMO", "1")
                .status().unwrap().success());
            return;
        }
        let cc = eframe::CreationContext::_new_kittest(Default::default());
        let mut app = VoiceApp::new(&cc);
        app.persona_form.open = true;
        app.persona_form.voice = "Uncle_Fu".into();
        app.persona_form.preview_request = Some(7);
        app.persona_form.preview_notice = "Playing".into();
        app.persona_preview_done(&json!({"id":6,"message":"late failure","error":true}));
        assert_eq!(app.persona_form.preview_notice, "Playing");
        app.complete_persona_preview(7, Ok(Value::Null));
        assert_eq!(app.persona_form.preview_request, Some(7));
        app.persona_preview_done(&json!({"id":7,"message":"synthesis failed","error":true}));
        assert_eq!(app.persona_form.preview_request, None);
        assert!(app.persona_form.preview_error);
        app.persona_form.preview_request = Some(8);
        app.stop_persona_preview();
        app.persona_preview_done(&json!({"id":8,"message":"late completion"}));
        assert_eq!(app.persona_form.preview_notice, "Preview stopped");
        app.persona_form.preview_request = Some(9);
        app.persona_preview_lost("Engine disconnected");
        assert!(app.persona_form.preview_error);
        assert_eq!(app.persona_form.preview_request, None);
        app.persona_form.preview_request = Some(10);
        app.complete_persona_preview(10, Err("Could not send request".into()));
        assert_eq!(app.persona_form.preview_notice, "Could not send request");
        assert_eq!(app.persona_form.preview_request, None);
        assert_eq!(app.persona_form.voice, "Uncle_Fu");
        assert!(app.persona_form.open);

        // List preview events keep draft fields intact and remain tied to one card.
        app.persona_form.open = false;
        app.persona_form.preview_persona = "santa".into();
        app.persona_form.preview_request = Some(11);
        app.preview_saved_persona("santa");
        assert_eq!(app.persona_form.preview_request, None);
        app.persona_preview_done(&json!({"id":11,"message":"stale sample"}));
        assert_eq!(app.persona_form.preview_notice, "Preview stopped");
        app.persona_form.preview_request = Some(12);
        app.persona_preview_done(&json!({"id":12,"message":"Preview finished"}));
        assert_eq!(app.persona_form.preview_persona, "santa");
        assert_eq!(app.persona_form.preview_notice, "Preview finished");
        assert_eq!(app.persona_form.voice, "Uncle_Fu");
        assert!(!app.persona_form.open);
    }
}
