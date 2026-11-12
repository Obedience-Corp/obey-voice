//! Application state and initialization; behavior lives in focused child modules.
mod actions;
mod demo;
mod events;
pub(crate) mod media;
mod media_view;
mod obey_catalog;
mod ollama_catalog;
mod persona_audio;
mod persona_directory;
mod persona_preview;
mod resident;
#[cfg(test)]
mod resident_tests;
mod setup;
mod view;

use crate::backend::Backend;
use crate::model::{KeySpec, Persona, PersonaForm, Purpose, Screen, Session, State};
use serde_json::Value;
use std::collections::{HashMap, VecDeque};

pub struct VoiceApp {
    pub media: media::Media,
    pub resident: crate::tray::Resident,
    pub setup: crate::setup::Setup,
    pub obey_catalog: Value,
    pub obey_catalog_request: Option<u64>,
    pub ollama_catalog: Value,
    pub ollama_catalog_key: Option<String>,
    pub ollama_catalog_request: Option<(u64, String)>,
    pub backend: Backend,
    pub demo: bool,
    pub dark: bool,
    pub connected: bool,
    pub screen: Screen,
    pub state: State,
    pub active: bool,
    pub starting: bool,
    pub status: String,
    pub error: Option<String>,
    pub next_id: u64,
    pub pending: HashMap<u64, Purpose>,
    pub sessions: Vec<Session>,
    pub personas: Vec<Persona>,
    pub schema: Vec<KeySpec>,
    pub values: Value,
    pub drafts: HashMap<String, String>,
    pub group: String,
    pub enroll_name: String,
    pub models_move_to: String,
    pub enroll_owner: bool,
    pub search: String,
    pub composer: String,
    pub activity: bool,
    pub show_think: bool,
    pub talk_held: bool,
    pub new_session: Option<String>,
    pub start_persona: String,
    pub text_only: bool,
    pub silent: bool,
    pub persona_form: PersonaForm,
    pub confirm: Option<(String, Vec<String>)>,
    pub library: Vec<Value>,
    pub library_query: String,
    pub book: Value,
    pub input: String,
    pub out_dir: String,
    pub voice: String,
    pub language: String,
    pub speed: String,
    pub format: String,
    pub resume_render: bool,
    pub from_library: bool,
    pub task: Option<u64>,
    pub task_title: String,
    pub logs: VecDeque<String>,
    pub remote: Option<u64>,
    pub remote_url: String,
    pub pairing: String,
    pub tailscale: bool,
    pub meeting_title: String,
    pub meeting_path: String,
    pub meeting_active: bool,
    pub meeting_paused: bool,
    pub meeting_note: String,
    pub meeting_lines: Vec<String>,
    pub meeting_partial: String,
    pub meeting_phase: String,
    pub meeting_level: f32,
    pub meeting_notes: Vec<String>,
    pub campaigns: Vec<Value>,
    pub campaign_error: Option<String>,
    pub diagnostics_open: bool,
    pub diagnostics_filter: String,
    pub meetings: Vec<Value>,
    pub meeting_result: Value,
    pub route_to: String,
    pub selected_worker: Option<String>,
    pub worker_config: Value,
    pub worker_revision: i64,
    pub engine_logs: VecDeque<String>,
    pub settings_persona: Value,
}
impl VoiceApp {
    pub fn new(cc: &eframe::CreationContext<'_>) -> Self {
        let ctx = &cc.egui_ctx;
        let dark = cc
            .storage
            .and_then(|s| s.get_string("appearance"))
            .as_deref()
            == Some("dark");
        crate::theme::setup(cc, dark);
        let demo = std::env::args().any(|a| a == "--demo")
            || std::env::var_os("OBEY_VOICE_AGENT_DEMO").is_some();
        let mut app = Self {
            media: media::Media::default(),
            resident: crate::tray::Resident::new(
                ctx.clone(),
                cc.storage
                    .and_then(|s| s.get_string("keep_in_tray"))
                    .as_deref()
                    != Some("false"),
            ),
            setup: crate::setup::Setup::default(),
            obey_catalog: Value::Null,
            obey_catalog_request: None,
            ollama_catalog: Value::Null,
            ollama_catalog_key: None,
            ollama_catalog_request: None,
            backend: Backend::new(ctx.clone(), demo),
            demo,
            dark,
            connected: demo,
            screen: if demo { Screen::Home } else { Screen::Setup },
            state: State::default(),
            active: false,
            starting: false,
            status: "Connecting to voice engine…".into(),
            error: None,
            next_id: 1,
            pending: HashMap::new(),
            sessions: vec![],
            personas: vec![],
            schema: vec![],
            values: Value::Null,
            drafts: HashMap::new(),
            group: "brain".into(),
            enroll_name: String::new(),
            models_move_to: String::new(),
            enroll_owner: true,
            search: String::new(),
            composer: String::new(),
            activity: false,
            show_think: false,
            talk_held: false,
            new_session: None,
            start_persona: String::new(),
            text_only: std::env::args().any(|a| a == "--text-only"),
            silent: std::env::args().any(|a| a == "--silent"),
            persona_form: PersonaForm::default(),
            confirm: None,
            library: vec![],
            library_query: String::new(),
            book: Value::Null,
            input: String::new(),
            out_dir: String::new(),
            voice: String::new(),
            language: String::new(),
            speed: String::new(),
            format: String::new(),
            resume_render: true,
            from_library: false,
            task: None,
            task_title: String::new(),
            logs: VecDeque::new(),
            remote: None,
            remote_url: String::new(),
            pairing: String::new(),
            tailscale: true,
            meeting_title: String::new(),
            meeting_path: String::new(),
            meeting_active: false,
            meeting_paused: false,
            meeting_note: String::new(),
            meeting_lines: vec![],
            meeting_partial: String::new(),
            meeting_phase: String::new(),
            meeting_level: 0.,
            meeting_notes: vec![],
            campaigns: vec![],
            campaign_error: None,
            diagnostics_open: false,
            diagnostics_filter: String::new(),
            meetings: vec![],
            meeting_result: Value::Null,
            route_to: String::new(),
            selected_worker: None,
            worker_config: Value::Null,
            worker_revision: 0,
            engine_logs: VecDeque::new(),
            settings_persona: Value::Null,
        };
        if demo {
            app.seed_demo();
        }
        app
    }
}
