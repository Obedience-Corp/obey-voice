use crate::app::VoiceApp;
use crate::backend::Event;
use crate::model::{PersonaForm, Purpose, Screen, display, route_receipt, string};
use serde_json::{Value, json};
use std::collections::VecDeque;

impl VoiceApp {
    pub(super) fn poll(&mut self) {
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
                    self.persona_preview_lost(&err);
                    self.stop_persona_audio_check();
                    self.connected = false;
                    self.setup.busy = None;
                    self.setup.invalidate();
                    self.starting = false;
                    self.active = false;
                    self.meeting_active = false;
                    self.meeting_paused = false;
                    // This engine can no longer acknowledge meeting controls.
                    // Keep independent CLI jobs, but release the tray transition
                    // and allow the next engine's meeting to start and save.
                    self.pending.retain(|_, purpose| {
                        !matches!(
                            purpose,
                            Purpose::MeetingPaused(_)
                                | Purpose::MeetingStart
                                | Purpose::MeetingStop
                        )
                    });
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
                    if self.task == Some(id)
                        && let Ok(v) = serde_json::from_str::<Value>(&line)
                        && v.get("phase").is_some()
                    {
                        self.setup.download = v;
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
                            self.setup_request(
                                "setup_status",
                                json!({}),
                                crate::setup::Operation::Load,
                            );
                        }
                        "state" => {
                            if (self.active || self.starting)
                                && let Ok(state) = serde_json::from_value(v["data"].clone())
                            {
                                self.state = state;
                            }
                        }
                        "persona_preview_done" => self.persona_preview_done(&v["data"]),
                        "progress" => self.status = v["data"].as_str().unwrap_or_default().into(),
                        "error" => self.error = Some(display(&v["data"])),
                        "meeting_utterance" => {
                            self.meeting_partial.clear();
                            self.meeting_lines.push(format!(
                                "{}  {}",
                                string(&v["data"], "At"),
                                string(&v["data"], "Text")
                            ));
                        }
                        "meeting_partial" => {
                            self.meeting_partial = v["data"].as_str().unwrap_or_default().into()
                        }
                        "meeting_phase" => {
                            self.meeting_phase = v["data"].as_str().unwrap_or_default().into()
                        }
                        "meeting_level" => {
                            self.meeting_level = v["data"].as_f64().unwrap_or_default() as f32
                        }
                        "meeting_saved" => {
                            self.meeting_result = v["data"].clone();
                            self.meeting_active = false;
                        }
                        "meeting_stop_requested" => {
                            self.stop_meeting();
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
        if let Purpose::ObeyCatalog(key) = &purpose {
            self.complete_obey_catalog(id, key.clone(), result);
            return;
        }
        if let Purpose::OllamaCatalog(key) = &purpose {
            self.ollama_catalog_result(id, key.clone(), result);
            return;
        }
        if let Purpose::Setup(operation) = purpose {
            if self.setup.busy != Some(id) {
                return;
            }
            self.complete_setup(operation, result);
            return;
        }
        if matches!(
            purpose,
            Purpose::PersonaDirectories | Purpose::PersonaBrowse
        ) {
            self.complete_persona_directory(id, purpose, result);
            return;
        }
        if matches!(purpose, Purpose::PersonaAudioCheck) {
            self.complete_persona_audio_check(id, result);
            return;
        }
        if matches!(purpose, Purpose::PersonaPreview) {
            self.complete_persona_preview(id, result);
            return;
        }
        let v = match result {
            Ok(v) => v,
            Err(err) => {
                if self.persona_form.open
                    && matches!(&purpose, Purpose::Mutation(label) if label.starts_with("Persona"))
                {
                    self.persona_form.save_error = err.clone();
                }
                if matches!(
                    purpose,
                    Purpose::Start | Purpose::Stop | Purpose::MeetingStart | Purpose::MeetingStop
                ) {
                    self.starting = false;
                }
                if matches!(purpose, Purpose::Campaigns) {
                    self.campaign_error = Some(err);
                } else {
                    self.error = Some(err);
                }
                return;
            }
        };
        match purpose {
            Purpose::ObeyCatalog(_)
            | Purpose::OllamaCatalog(_)
            | Purpose::Setup(_)
            | Purpose::PersonaDirectories
            | Purpose::PersonaBrowse
            | Purpose::PersonaPreview
            | Purpose::PersonaAudioCheck => {
                unreachable!()
            }
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
                let route_before = self.obey_route_key();
                self.values = v["values"].clone();
                if route_before != self.obey_route_key()
                    || self.values["brain_provider"]["value"] != "obey"
                {
                    self.obey_catalog = Value::Null;
                    self.obey_catalog_request = None;
                }
                if self.values["brain_provider"]["value"] != "ollama" {
                    self.invalidate_ollama_catalog();
                }
                self.settings_persona = v["persona"].clone();
                if self.values["brain_provider"]["value"] == "obey"
                    && matches!(self.screen, Screen::Settings | Screen::Setup)
                    && self.obey_catalog.is_null()
                    && self.obey_catalog_request.is_none()
                {
                    self.load_obey_catalog(false);
                }
                if self.active && self.screen == Screen::Settings {
                    self.request("settings_refresh", json!({}), Purpose::Ignore);
                }
            }
            Purpose::SettingSave(key) => {
                self.setup.invalidate();
                self.obey_catalog = Value::Null;
                self.obey_catalog_request = None;
                self.invalidate_ollama_catalog();
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
                self.stop_persona_preview();
                self.stop_persona_audio_check();
                let p = &v["persona"];
                self.persona_form = PersonaForm {
                    audio: crate::persona_audio::AudioDraft::load(&p["audio"]),
                    starting_directory: string(p, "starting_directory"),
                    original_directory: string(p, "starting_directory"),
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
                    ..Default::default()
                };
                self.load_persona_directories();
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
                self.starting = self
                    .pending
                    .values()
                    .any(|p| matches!(p, Purpose::MeetingStop));
                self.meeting_active = true;
                self.meeting_paused = false;
                self.meeting_path = string(&v, "path");
                if !self.starting {
                    self.status = "Recording meeting".into();
                }
            }
            Purpose::MeetingStop => {
                self.starting = false;
                self.meeting_active = false;
                self.meeting_result = v;
                self.status = "Meeting saved".into();
                self.command(&["meeting", "list", "--json"], Purpose::Meetings);
            }
            Purpose::Campaigns => {
                self.campaigns = serde_json::from_value(v).unwrap_or_default();
                self.campaign_error = None;
            }
            Purpose::MeetingPaused(paused) => {
                self.meeting_paused = paused;
                self.meeting_partial.clear();
                self.meeting_level = 0.;
            }
            Purpose::MeetingNote(note) => {
                if self.meeting_note == note {
                    self.meeting_note.clear();
                }
                self.meeting_notes.push(note);
            }
            Purpose::MeetingRoute => {
                crate::diagnostics::record(
                    if v["outcome"] == "failed" {
                        "error"
                    } else {
                        "info"
                    },
                    "meeting.route",
                    v.clone(),
                );
                match route_receipt(&v) {
                    Ok(message) => self.status = message,
                    Err(error) => {
                        self.status = "Meeting notes kept locally".into();
                        self.error = Some(error);
                    }
                }
                push_log(
                    &mut self.logs,
                    serde_json::to_string_pretty(&v).unwrap_or_default(),
                );
                self.command(&["meeting", "list", "--json"], Purpose::Meetings);
            }
            Purpose::Task => {
                self.status = "Completed".into();
                // A task such as a model move can change saved settings.
                self.command(&["config", "get", "--json"], Purpose::SettingsValues);
                push_log(
                    &mut self.logs,
                    serde_json::to_string_pretty(&v).unwrap_or_default(),
                );
            }
            Purpose::Ignore => {}
        }
    }
}
fn push_log(logs: &mut VecDeque<String>, line: String) {
    if logs.len() >= 400 {
        logs.pop_front();
    }
    logs.push_back(line);
}
