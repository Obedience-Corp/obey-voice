use crate::app::VoiceApp;
use crate::model::{Persona, Session, State, Turn};

impl VoiceApp {
    pub(super) fn seed_demo(&mut self) {
        self.status = "Demo · no microphone or saved changes".into();
        self.personas = vec![
            Persona {
                id: "obey".into(),
                display_name: "Obey".into(),
                provider: "kokoro".into(),
                voice: "af_heart".into(),
                active: true,
                ..Default::default()
            },
            Persona {
                id: "research".into(),
                display_name: "Research companion".into(),
                provider: "qwen3-tts".into(),
                voice: "Ryan".into(),
                ..Default::default()
            },
        ];
        self.sessions = vec![Session {
            id: "demo-session".into(),
            summary: "Plan the desktop voice workflow".into(),
            provider: "ollama".into(),
            model: "local model".into(),
            updated_at: "Demo session".into(),
            ..Default::default()
        }];
        self.state=State{name:"Obey".into(),session_id:"demo-session".into(),recording:"off".into(),turns:vec![Turn{role:"user".into(),content:"Let’s plan the desktop voice workflow.".into(),name:"You".into(),..Default::default()},Turn{role:"assistant".into(),content:"We can start with a conversation, then open personas or settings and return to the same chat. Your microphone pauses while you browse.".into(),name:"Obey".into(),..Default::default()}],..Default::default()};
    }
}
