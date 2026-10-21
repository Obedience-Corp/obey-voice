use crate::app::VoiceApp;
use crate::model::{Purpose, Screen, State, string};
use crate::setup::{Operation, Progress};
use serde_json::{Value, json};

impl VoiceApp {
    pub fn open_setup(&mut self) {
        if self.active || self.starting || self.meeting_active {
            return;
        }
        self.setup.invalidate();
        self.setup.progress.step = 0;
        self.screen = Screen::Setup;
        self.command(&["config", "schema", "--json"], Purpose::SettingsSchema);
        self.command(&["config", "get", "--json"], Purpose::SettingsValues);
    }
    pub fn setup_request(&mut self, method: &str, args: Value, operation: Operation) {
        self.setup.error = None;
        self.setup.busy = Some(self.request(method, args, Purpose::Setup(operation)));
    }
    pub fn setup_save(&mut self, step: usize, completed: bool, deferred: bool) {
        if step == 4 && self.active && !self.setup.progress.text_only {
            self.action("mic", "", false);
        }
        let p = Progress {
            step,
            completed,
            deferred,
            ..self.setup.progress.clone()
        };
        self.setup_request(
            "setup_save",
            json!({"config":p}),
            Operation::Save(p.clone()),
        );
    }
    pub fn setup_models(&mut self, install: bool) {
        self.setup.error = None;
        self.setup.models = None;
        let Some(args) = self
            .setup
            .model_args(if install { "ensure" } else { "status" })
        else {
            self.setup.models = Some(vec![]);
            return;
        };
        let operation = if install {
            Operation::Install
        } else {
            Operation::Models
        };
        if install {
            self.setup.download_cancelled = false;
            self.logs.clear();
            self.setup.download = Value::Null;
        }
        let id = self.cli(args, Purpose::Setup(operation), install);
        self.setup.busy = Some(id);
        if install {
            self.task = Some(id);
            self.task_title = "Prepare speech models".into();
        }
    }
    pub fn setup_start(&mut self) {
        self.setup.reply_received = false;
        self.setup.sent_test = false;
        self.setup.mic_confirmed = false;
        self.setup.speaker_confirmed = false;
        self.state = State::default();
        self.starting = true;
        self.error = None;
        self.status = "Preparing your test conversation…".into();
        self.setup_request(
            "start",
            json!({"text_only":self.setup.progress.text_only,
            "silent":self.setup.progress.silent}),
            Operation::Start,
        );
    }
    pub fn setup_send_test(&mut self) {
        self.setup.reply_baseline = self
            .state
            .turns
            .iter()
            .filter(|t| t.role == "assistant")
            .count();
        self.setup.sent_test = true;
        self.setup.reply_received = false;
        self.action(
            "text",
            "Say a short hello so I can check that this conversation is working. Do not use tools.",
            false,
        );
    }
    pub fn setup_observe(&mut self) {
        if self.screen != Screen::Setup || !self.active {
            return;
        }
        if self.setup.sent_test && !self.state.busy && !self.state.error {
            let replies = self
                .state
                .turns
                .iter()
                .filter(|t| t.role == "assistant" && !t.content.trim().is_empty())
                .count();
            if replies > self.setup.reply_baseline {
                self.setup.reply_received = true;
            }
        }
        if self.state.error {
            self.setup.reply_received = false;
        }
    }
    pub(super) fn complete_setup(&mut self, operation: Operation, result: Result<Value, String>) {
        self.setup.busy = None;
        if matches!(operation, Operation::Install) && self.setup.download_cancelled {
            self.setup.error =
                Some("Download cancelled. Check speech models to resume installation.".into());
            return;
        }
        let value = match result {
            Ok(v) => v,
            Err(error) => {
                self.starting = false;
                self.setup.error = Some(crate::setup::error_message(&error));
                return;
            }
        };
        match operation {
            Operation::Load => {
                self.setup.loaded = true;
                self.setup.existing = value["existing"] == true;
                self.setup.config_path = string(&value, "config_path");
                self.setup.warning = string(&value, "warning");
                self.setup.progress =
                    serde_json::from_value(value["progress"].clone()).unwrap_or_default();
                self.setup.progress.text_only |= std::env::args().any(|a| a == "--text-only");
                self.setup.progress.silent |= std::env::args().any(|a| a == "--silent");
                self.text_only = self.setup.progress.text_only;
                self.silent = self.setup.progress.silent;
                if self.setup.progress.completed || self.setup.progress.deferred {
                    self.screen = Screen::Home;
                    self.refresh();
                } else {
                    self.screen = Screen::Setup;
                    self.command(&["config", "schema", "--json"], Purpose::SettingsSchema);
                    self.command(&["config", "get", "--json"], Purpose::SettingsValues);
                }
            }
            Operation::Save(p) => {
                self.setup.progress = p;
                self.text_only = self.setup.progress.text_only;
                self.silent = self.setup.progress.silent;
                if self.setup.progress.completed || self.setup.progress.deferred {
                    self.screen = Screen::Home;
                    self.status = if self.setup.progress.completed {
                        "Setup complete"
                    } else {
                        "Setup skipped; readiness not verified"
                    }
                    .into();
                    self.refresh();
                }
            }
            Operation::Provider => self.setup.provider = Some(value),
            Operation::Models => {
                match serde_json::from_value::<Option<Vec<Value>>>(value) {
                    Ok(items) => self.setup.models = Some(items.unwrap_or_default()),
                    Err(_) => self.setup.error = Some(
                        "The engine returned an unreadable model list. Open Diagnostics and retry."
                            .into(),
                    ),
                }
            }
            Operation::Install => self.setup_models(false),
            Operation::Start => {
                self.active = true;
                self.starting = false;
                self.status = "Test conversation open".into();
                if self.setup.progress.step == 4 {
                    self.setup_save(3, false, false);
                }
            }
            Operation::Stop => {
                self.active = false;
                self.starting = false;
                self.setup.invalidate();
                self.setup_save(1, false, false);
            }
            Operation::Finish => {
                self.active = false;
                self.starting = false;
                self.setup_save(4, true, false);
            }
        }
    }
}
