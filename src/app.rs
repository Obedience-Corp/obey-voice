use crate::{
    backend::{Backend, Event},
    model::*,
};
use eframe::egui::{self, Color32, RichText};
use serde_json::{Value, json};
use std::{
    collections::{HashMap, VecDeque},
    time::Duration,
};

use crate::theme::{accent, muted};
pub struct VoiceApp {
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
            backend: Backend::new(ctx.clone(), demo),
            demo,
            dark,
            connected: demo,
            screen: Screen::Home,
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
            group: "voice".into(),
            enroll_name: String::new(),
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
    pub fn request(&mut self, method: &str, mut args: Value, purpose: Purpose) -> u64 {
        let id = self.next_id;
        self.next_id += 1;
        if self.demo {
            self.status = format!("Demo: {method}");
            return id;
        }
        args["method"] = json!(method);
        args["id"] = json!(id);
        self.pending.insert(id, purpose);
        self.backend.request(args);
        id
    }
    pub fn action(&mut self, method: &str, text: &str, enabled: bool) {
        self.request(
            method,
            json!({"text":text,"enabled":enabled}),
            Purpose::Ignore,
        );
    }
    pub fn cli(&mut self, args: Vec<String>, purpose: Purpose, stream: bool) -> u64 {
        let id = self.next_id;
        self.next_id += 1;
        if self.demo {
            self.status = "Demo mode: changes are not written".into();
            return id;
        }
        self.pending.insert(id, purpose);
        self.backend.cli(id, args, stream);
        id
    }
    pub fn command(&mut self, args: &[&str], purpose: Purpose) {
        self.cli(args.iter().map(|s| (*s).into()).collect(), purpose, false);
    }
    pub fn refresh(&mut self) {
        if !self.demo {
            self.request("sessions", json!({}), Purpose::Sessions);
            self.command(&["persona", "list", "--json"], Purpose::Personas);
        }
    }
    pub fn navigate(&mut self, target: Screen) {
        if self.screen == target {
            return;
        }
        if self.remote.is_some() && target != Screen::Remote {
            self.stop_remote();
        }
        if self.active {
            if self.screen.manager() {
                self.action("return", "", false);
            }
            if target.manager() {
                self.action(
                    "manager",
                    match target {
                        Screen::Settings => "settings",
                        Screen::Workers => "workers",
                        _ => "personas",
                    },
                    false,
                );
            } else if target != Screen::Chat {
                self.request("stop", json!({}), Purpose::Stop);
                self.starting = !self.demo;
                if self.demo {
                    self.active = false;
                }
                self.status = if self.demo {
                    "Preview mode"
                } else {
                    "Saving conversation…"
                }
                .into();
            }
        }
        self.screen = target;
        self.search.clear();
        match target {
            Screen::Home | Screen::Sessions => self.refresh(),
            Screen::Personas => self.command(&["persona", "list", "--json"], Purpose::Personas),
            Screen::Settings => {
                self.command(&["config", "schema", "--json"], Purpose::SettingsSchema);
                self.command(&["config", "get", "--json"], Purpose::SettingsValues);
            }
            Screen::Workers => {
                self.request("worker_config_get", json!({}), Purpose::WorkerConfig);
            }
            Screen::Library => self.load_library(),
            Screen::Meeting => self.command(&["meeting", "list", "--json"], Purpose::Meetings),
            _ => {}
        }
    }
    pub fn begin_chat(&mut self, session: String) {
        if self.demo {
            self.active = true;
            self.screen = Screen::Chat;
            return;
        }
        self.screen = Screen::Chat;
        self.starting = true;
        self.state = State::default();
        self.error = None;
        self.status = "Preparing conversation…".into();
        self.request("start",json!({"session_id":session,"persona_id":self.start_persona,"text_only":self.text_only,"silent":self.silent}),Purpose::Start);
    }
    pub fn submit(&mut self, ctx: &egui::Context) {
        let text = self.composer.trim().to_owned();
        if text.is_empty() {
            return;
        }
        self.composer.clear();
        match text.as_str() {
            "/settings" => self.navigate(Screen::Settings),
            "/personas" => self.navigate(Screen::Personas),
            "/workers" => self.navigate(Screen::Workers),
            "/menu" | "/back" => self.navigate(Screen::Home),
            "/activity" | "/timeline" => self.activity = !self.activity,
            "/vim" | "/vim on" | "/vim off" | "/vim insert" => {
                self.status = "The desktop composer uses native text editing; Vim mode is available in the terminal app".into();
            }
            "/think" | "/thinking" => self.show_think = !self.show_think,
            "/think on" | "/thinking on" => self.show_think = true,
            "/think off" | "/thinking off" => self.show_think = false,
            "/copy" | "/yank" => {
                if let Some(result) = self.state.work_results.last() {
                    ctx.copy_text(result.clone());
                    return;
                }
                if let Some(t) = self
                    .state
                    .turns
                    .iter()
                    .rev()
                    .find(|t| t.role == "assistant")
                {
                    ctx.copy_text(t.content.clone());
                }
            }
            "/copy all" | "/yank all" => ctx.copy_text(
                self.state
                    .turns
                    .iter()
                    .map(|t| {
                        format!(
                            "{}: {}",
                            if t.name.is_empty() { &t.role } else { &t.name },
                            t.content
                        )
                    })
                    .collect::<Vec<_>>()
                    .join("\n\n"),
            ),
            "/quit" | "/q" | "/exit" => ctx.send_viewport_cmd(egui::ViewportCommand::Close),
            _ => self.action("text", &text, false),
        }
    }
    pub fn load_library(&mut self) {
        let args = if self.library_query.trim().is_empty() {
            vec!["library".into(), "list".into(), "--json".into()]
        } else {
            vec![
                "library".into(),
                "search".into(),
                self.library_query.clone(),
                "--json".into(),
            ]
        };
        self.cli(args, Purpose::Library, false);
    }
    pub fn start_task(&mut self, title: &str, args: Vec<String>) {
        if self.task.is_some() {
            return;
        }
        self.logs.clear();
        self.task_title = title.into();
        let id = self.cli(args, Purpose::Task, true);
        if !self.demo {
            self.task = Some(id);
        }
    }
    pub fn stop_remote(&mut self) {
        if let Some(id) = self.remote.take() {
            self.backend.cancel(id);
        }
        self.remote_url.clear();
        self.pairing.clear();
    }
    fn poll(&mut self) {
        for _ in 0..300 {
            let Ok(event) = self.backend.events.try_recv() else {
                break;
            };
            match event {
                Event::ProtocolWarning(generation, err) => {
                    if generation == self.backend.generation {
                        self.error = Some(err);
                    }
                }
                Event::Lost(generation, err) => {
                    if generation != self.backend.generation {
                        continue;
                    }
                    self.connected = false;
                    self.starting = false;
                    self.active = false;
                    self.error = Some(err);
                }
                Event::Log { id, line } => {
                    if self.remote == Some(id)
                        && let Ok(v) = serde_json::from_str::<Value>(&line)
                    {
                        match v["event"].as_str() {
                            Some("ready") => {
                                self.remote_url = string(&v, "url");
                                self.status = "Remote server ready".into();
                            }
                            Some("pairing_code") => self.pairing = string(&v, "code"),
                            _ => {}
                        }
                    }
                    if id == 0 {
                        push_log(&mut self.engine_logs, line);
                    } else {
                        push_log(&mut self.logs, line);
                    }
                }
                Event::Result { id, result } => self.complete(id, result),
                Event::Bridge(generation, v) => {
                    if generation != self.backend.generation {
                        continue;
                    }
                    if let Some(id) = v["id"].as_u64() {
                        let result = if let Some(err) = v["error"].as_str() {
                            Err(err.into())
                        } else {
                            Ok(v["result"].clone())
                        };
                        self.complete(id, result);
                        continue;
                    }
                    match v["event"].as_str().unwrap_or_default() {
                        "ready" => {
                            self.connected = true;
                            self.status = "Ready".into();
                            self.refresh();
                        }
                        "state" => {
                            if (self.active || self.starting)
                                && let Ok(state) = serde_json::from_value(v["data"].clone())
                            {
                                self.state = state;
                            }
                        }
                        "progress" => self.status = v["data"].as_str().unwrap_or_default().into(),
                        "error" => self.error = Some(display(&v["data"])),
                        "meeting_utterance" => self.meeting_lines.push(string(&v["data"], "Text")),
                        "meeting_partial" => {
                            self.status = v["data"].as_str().unwrap_or_default().into()
                        }
                        "meeting_saved" => {
                            self.meeting_result = v["data"].clone();
                            self.meeting_active = false;
                        }
                        "meeting_stop_requested" => {
                            self.request("meeting_stop", json!({}), Purpose::MeetingStop);
                            self.status = "Saving meeting…".into();
                        }
                        _ => {}
                    }
                }
            }
        }
    }
    fn complete(&mut self, id: u64, result: Result<Value, String>) {
        let Some(purpose) = self.pending.remove(&id) else {
            return;
        };
        if self.task == Some(id) {
            self.task = None;
        }
        if self.remote == Some(id) {
            self.remote = None;
            self.remote_url.clear();
            self.pairing.clear();
        }
        let v = match result {
            Ok(v) => v,
            Err(err) => {
                if matches!(
                    purpose,
                    Purpose::Start | Purpose::Stop | Purpose::MeetingStart | Purpose::MeetingStop
                ) {
                    self.starting = false;
                }
                self.error = Some(err);
                return;
            }
        };
        match purpose {
            Purpose::Sessions => self.sessions = serde_json::from_value(v).unwrap_or_default(),
            Purpose::Personas => self.personas = serde_json::from_value(v).unwrap_or_default(),
            Purpose::Start => {
                self.active = true;
                self.starting = false;
                self.status = "Conversation ready".into();
            }
            Purpose::Stop => {
                self.active = false;
                self.starting = false;
                self.status = "Conversation saved".into();
                self.refresh();
            }
            Purpose::WorkerConfig => {
                self.worker_revision = v["revision"].as_i64().unwrap_or(0);
                self.worker_config = v;
            }
            Purpose::SettingsSchema => {
                self.schema = serde_json::from_value(v["keys"].clone()).unwrap_or_default()
            }
            Purpose::SettingsValues => {
                let before = &self.values;
                self.drafts
                    .retain(|key, value| *value != display(&before[key]["value"]));
                self.values = v["values"].clone();
                self.settings_persona = v["persona"].clone();
                if self.active && self.screen == Screen::Settings {
                    self.request("settings_refresh", json!({}), Purpose::Ignore);
                }
            }
            Purpose::SettingSave(key) => {
                self.drafts.remove(&key);
                self.status = format!("Saved {key}");
                self.command(&["config", "get", "--json"], Purpose::SettingsValues);
            }
            Purpose::Mutation(label) => {
                self.status = label;
                if v.get("created").and_then(Value::as_bool) == Some(true) {
                    self.persona_form.id = string(&v["persona"], "id");
                    self.save_persona();
                } else {
                    self.persona_form.open = false;
                }
                self.refresh();
            }
            Purpose::PersonaLoad => {
                let p = &v["persona"];
                self.persona_form = PersonaForm {
                    id: string(p, "id"),
                    name: string(p, "display_name"),
                    brain: string(&p["brain"], "provider"),
                    model: string(&p["brain"], "model"),
                    tts: string(&p["tts"], "provider"),
                    voice: string(&p["tts"], "voice"),
                    tier: string(&p["tts"], "tier"),
                    prompt: string(&v["prompt"], "body"),
                    structured: v["prompt"]["structured"].as_bool().unwrap_or(false),
                    open: true,
                };
            }
            Purpose::Library => {
                self.library = v
                    .as_array()
                    .cloned()
                    .unwrap_or_else(|| v["books"].as_array().cloned().unwrap_or_default())
            }
            Purpose::Book => self.book = v,
            Purpose::Meetings => {
                self.meetings = v["meetings"].as_array().cloned().unwrap_or_default()
            }
            Purpose::MeetingDocument => self.meeting_result = v,
            Purpose::MeetingStart => {
                self.starting = false;
                self.meeting_active = true;
                self.meeting_paused = false;
                self.meeting_path = string(&v, "path");
                self.status = "Recording meeting".into();
            }
            Purpose::MeetingStop => {
                self.starting = false;
                self.meeting_active = false;
                self.meeting_result = v;
                self.status = "Meeting saved".into();
                self.command(&["meeting", "list", "--json"], Purpose::Meetings);
            }
            Purpose::Task => {
                self.status = "Completed".into();
                push_log(
                    &mut self.logs,
                    serde_json::to_string_pretty(&v).unwrap_or_default(),
                );
            }
            Purpose::Ignore => {}
        }
    }
    pub fn save_persona(&mut self) {
        let f = &self.persona_form;
        if f.id.is_empty() {
            self.cli(
                vec![
                    "persona".into(),
                    "create".into(),
                    f.name.clone(),
                    "--prompt".into(),
                    f.prompt.clone(),
                    "--no-activate".into(),
                    "--json".into(),
                ],
                Purpose::Mutation("Persona created".into()),
                false,
            );
            return;
        }
        let mut args = vec![
            "persona".into(),
            "edit".into(),
            f.id.clone(),
            "--display-name".into(),
            f.name.clone(),
            "--brain-provider".into(),
            f.brain.clone(),
            "--brain-model".into(),
            f.model.clone(),
            "--tts-provider".into(),
            f.tts.clone(),
            "--voice".into(),
            f.voice.clone(),
            "--tier".into(),
            f.tier.clone(),
            "--json".into(),
        ];
        if !f.structured && !f.prompt.is_empty() {
            args.extend(["--prompt".into(), f.prompt.clone()]);
        }
        self.cli(
            args,
            Purpose::Mutation(
                "Persona saved; model and voice changes apply to the next conversation".into(),
            ),
            false,
        );
    }
    fn seed_demo(&mut self) {
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
pub fn push_log(logs: &mut VecDeque<String>, line: String) {
    if logs.len() >= 400 {
        logs.pop_front();
    }
    logs.push_back(line);
}
impl eframe::App for VoiceApp {
    fn save(&mut self, storage: &mut dyn eframe::Storage) {
        storage.set_string(
            "appearance",
            if self.dark { "dark" } else { "light" }.into(),
        );
    }
    fn clear_color(&self, _: &egui::Visuals) -> [f32; 4] {
        [0.; 4]
    }
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        self.poll();
        let ctx = ui.ctx().clone();
        if ctx.input(|i| i.key_pressed(egui::Key::Escape)) && self.screen.manager() && self.active {
            self.navigate(Screen::Chat);
        }
        if self.active && self.screen == Screen::Chat {
            if ctx.input_mut(|i| i.consume_key(egui::Modifiers::CTRL, egui::Key::G)) {
                self.action("mic", "", !self.state.mic);
            }
            if ctx.input_mut(|i| i.consume_key(egui::Modifiers::CTRL, egui::Key::O)) {
                self.action("audio", "", self.state.muted);
            }
            if ctx.input_mut(|i| i.consume_key(egui::Modifiers::CTRL, egui::Key::T)) {
                self.activity = !self.activity;
            }
            if ctx.input_mut(|i| i.consume_key(egui::Modifiers::CTRL, egui::Key::K)) {
                self.show_think = !self.show_think;
            }
        }
        crate::theme::backdrop(ui);
        egui::Panel::left("navigation")
            .exact_size(222.)
            .resizable(false)
            .frame(egui::Frame::new().inner_margin(16))
            .show(ui, |ui| {
                // Native traffic lights occupy the upper-left; there is no title bar.
                ui.add_space(36.);
                ui.horizontal(|ui| {
                    let (r, _) = ui.allocate_exact_size(egui::vec2(34., 40.), egui::Sense::hover());
                    crate::theme::waveform(ui.painter(), r.center(), 0.55, accent(ui));
                    ui.vertical(|ui| {
                        ui.label(RichText::new("Obey").size(23.).strong());
                        ui.label(RichText::new("Voice workspace").size(12.).color(muted(ui)));
                    });
                });
                ui.add_space(26.);
                ui.label(RichText::new("WORKSPACE").size(10.).color(muted(ui)));
                ui.add_space(2.);
                ui.spacing_mut().item_spacing.y = 3.;
                for screen in Screen::ALL {
                    if screen == Screen::Chat && !self.active && !self.starting {
                        continue;
                    }
                    if screen == Screen::Settings {
                        continue;
                    }
                    if screen == Screen::Meeting {
                        ui.add_space(22.);
                        ui.label(RichText::new("TOOLS").size(10.).color(muted(ui)));
                        ui.add_space(5.);
                    }
                    let enabled =
                        !self.starting && (!self.meeting_active || screen == Screen::Meeting);
                    let clicked = ui
                        .add_enabled_ui(enabled, |ui| {
                            crate::theme::nav(ui, screen, self.screen == screen)
                        })
                        .inner
                        .clicked();
                    if clicked {
                        self.navigate(screen);
                    }
                }
                // Blank space is a drag handle; interactive controls keep normal input.
                let gap = (ui.available_height() - 160.).max(0.);
                let (_, drag) = ui.allocate_exact_size(
                    egui::vec2(ui.available_width(), gap),
                    egui::Sense::drag(),
                );
                if drag.drag_started() {
                    ctx.send_viewport_cmd(egui::ViewportCommand::StartDrag);
                }
                let enabled = !self.starting && !self.meeting_active;
                if ui
                    .add_enabled_ui(enabled, |ui| {
                        crate::theme::nav(ui, Screen::Settings, self.screen == Screen::Settings)
                    })
                    .inner
                    .clicked()
                {
                    self.navigate(Screen::Settings);
                }
                ui.add_space(12.);
                ui.horizontal(|ui| {
                    let previous = self.dark;
                    ui.selectable_value(&mut self.dark, false, "Light");
                    ui.selectable_value(&mut self.dark, true, "Dark");
                    if previous != self.dark {
                        crate::theme::apply(&ctx, self.dark);
                        #[cfg(target_os = "macos")]
                        crate::theme::native_appearance(_frame, self.dark);
                        if let Some(storage) = _frame.storage_mut() {
                            storage.set_string(
                                "appearance",
                                if self.dark { "dark" } else { "light" }.into(),
                            );
                            storage.flush();
                        }
                    }
                });
                ui.add_space(10.);
                ui.horizontal(|ui| {
                    let (r, _) = ui.allocate_exact_size(egui::vec2(9., 9.), egui::Sense::hover());
                    ui.painter().circle_filled(
                        r.center(),
                        3.,
                        if self.connected {
                            Color32::from_rgb(65, 158, 128)
                        } else {
                            muted(ui)
                        },
                    );
                    ui.label(
                        RichText::new(if self.demo {
                            "Preview mode"
                        } else if self.connected {
                            "Voice engine connected"
                        } else {
                            "Engine offline"
                        })
                        .size(11.)
                        .color(muted(ui)),
                    );
                });
                if !self.connected && ui.button("Reconnect").clicked() {
                    self.backend.reset();
                    self.error = None;
                }
            });
        egui::CentralPanel::default()
            .frame(egui::Frame::new().inner_margin(egui::Margin {
                left: 12,
                right: 12,
                top: 12,
                bottom: 12,
            }))
            .show(ui, |ui| {
                crate::theme::glass(self.dark)
                    .fill(crate::theme::palette(self.dark).surface)
                    .inner_margin(28)
                    .corner_radius(24)
                    .show(ui, |ui| {
                        ui.set_min_size(ui.available_size());
                        // The page heading is part of the content, with no toolbar strip.
                        ui.horizontal(|ui| {
                            let heading = ui.heading(self.screen.title());
                            let drag = ui.interact(
                                heading.rect,
                                ui.scope_id().with("heading_drag"),
                                egui::Sense::drag(),
                            );
                            if drag.drag_started() {
                                ctx.send_viewport_cmd(egui::ViewportCommand::StartDrag);
                            }
                            if self.active
                                && self.screen.manager()
                                && ui.button("Back to conversation").clicked()
                            {
                                self.navigate(Screen::Chat);
                            }
                            ui.with_layout(
                                egui::Layout::right_to_left(egui::Align::Center),
                                |ui| {
                                    if self.starting || self.task.is_some() {
                                        ui.spinner();
                                    } else {
                                        ui.label(
                                            RichText::new(if self.demo {
                                                "PREVIEW"
                                            } else {
                                                "OBEY VOICE"
                                            })
                                            .size(10.)
                                            .color(muted(ui)),
                                        );
                                    }
                                },
                            );
                        });
                        ui.add_space(18.);
                        if let Some(err) = self.error.clone() {
                            egui::Frame::new()
                                .fill(Color32::from_rgb(255, 234, 234))
                                .inner_margin(12)
                                .corner_radius(12)
                                .show(ui, |ui| {
                                    ui.horizontal_wrapped(|ui| {
                                        ui.label(
                                            RichText::new(&err)
                                                .color(Color32::from_rgb(167, 49, 66)),
                                        );
                                        if ui.small_button("Dismiss").clicked() {
                                            self.error = None;
                                        }
                                    });
                                });
                            ui.add_space(10.);
                        }
                        egui::Panel::bottom("page_status")
                            .frame(egui::Frame::new().inner_margin(egui::Margin {
                                top: 10,
                                ..Default::default()
                            }))
                            .show(ui, |ui| {
                                ui.label(RichText::new(&self.status).size(11.).color(muted(ui)));
                            });
                        self.render_screen(ui);
                    });
            });
        self.dialogs(&ctx);
        if self.starting || self.active || self.meeting_active {
            ctx.request_repaint_after(Duration::from_millis(100));
        }
    }
}
