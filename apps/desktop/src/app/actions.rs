use crate::app::VoiceApp;
use crate::model::{Purpose, Screen, State};
use eframe::egui;
use serde_json::{Value, json};

impl VoiceApp {
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
        crate::diagnostics::record("trace", "navigation", json!({"screen":target.title()}));
        self.screen = target;
        self.search.clear();
        match target {
            Screen::Home | Screen::Sessions => self.refresh(),
            Screen::Personas => {
                self.command(&["persona", "list", "--json"], Purpose::Personas);
                self.command(&["config", "schema", "--json"], Purpose::SettingsSchema);
            }
            Screen::Settings => {
                self.command(&["config", "schema", "--json"], Purpose::SettingsSchema);
                self.command(&["config", "get", "--json"], Purpose::SettingsValues);
            }
            Screen::Workers => {
                self.request("worker_config_get", json!({}), Purpose::WorkerConfig);
            }
            Screen::Library => self.load_library(),
            Screen::Meeting => {
                self.command(&["meeting", "list", "--json"], Purpose::Meetings);
                self.command(&["--campaigns"], Purpose::Campaigns);
            }
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
            "/quit" | "/q" | "/exit" => self.quit(ctx),
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
    pub fn save_persona(&mut self) {
        self.persona_form.save_error.clear();
        let f = &self.persona_form;
        if f.id.is_empty() {
            self.cli(
                vec![
                    "persona".into(),
                    "create".into(),
                    f.name.clone(),
                    "--prompt".into(),
                    f.prompt.clone(),
                    "--starting-directory".into(),
                    f.starting_directory.clone(),
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
        if f.starting_directory != f.original_directory {
            args.extend(["--starting-directory".into(), f.starting_directory.clone()]);
        }
        if !f.structured && !f.prompt.is_empty() {
            args.extend(["--prompt".into(), f.prompt.clone()]);
        }
        self.cli(
            args,
            Purpose::Mutation(
                "Persona saved; starting directory applies to new conversations".into(),
            ),
            false,
        );
    }
}
