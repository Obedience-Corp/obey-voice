use crate::{
    app::VoiceApp,
    model::*,
    theme::{accent, card as card_color, muted, selected},
};
use eframe::egui::{self, Color32, RichText};
use serde_json::{Value, json};

fn card(ui: &mut egui::Ui, add: impl FnOnce(&mut egui::Ui)) {
    crate::theme::glass(ui.visuals().dark_mode).show(ui, |ui| {
        ui.set_width(ui.available_width());
        add(ui);
    });
}
fn field(ui: &mut egui::Ui, label: &str, value: &mut String) {
    ui.label(RichText::new(label).color(muted(ui)));
    ui.add(egui::TextEdit::singleline(value).desired_width(f32::INFINITY));
}
fn combo(ui: &mut egui::Ui, id: &str, value: &mut String, options: &[&str]) {
    egui::ComboBox::from_id_salt(id)
        .selected_text(if value.is_empty() {
            "App default"
        } else {
            value.as_str()
        })
        .show_ui(ui, |ui| {
            for option in options {
                ui.selectable_value(
                    value,
                    (*option).into(),
                    if option.is_empty() {
                        "App default"
                    } else {
                        option
                    },
                );
            }
        });
}
impl VoiceApp {
    pub fn render_screen(&mut self, ui: &mut egui::Ui) {
        match self.screen {
            Screen::Home => self.home(ui),
            Screen::Chat => self.chat(ui),
            Screen::Sessions => self.session_list(ui),
            Screen::Personas => self.persona_list(ui),
            Screen::Workers => self.workers(ui),
            Screen::Settings => self.settings(ui),
            Screen::Library => self.library_screen(ui),
            Screen::Audiobook => self.audiobook(ui),
            Screen::Remote => self.remote_screen(ui),
            Screen::Meeting => self.meeting(ui),
        }
    }
    fn home(&mut self, ui: &mut egui::Ui) {
        egui::ScrollArea::vertical().show(ui, |ui| {
            card(ui, |ui| {
                let r = ui.available_rect_before_wrap();
                if r.width() > 600. {
                    crate::theme::orb(
                        ui.painter(),
                        egui::pos2(r.right() - 88., r.top() + 84.),
                        65.,
                        ui.visuals().dark_mode,
                    );
                }
                ui.add_space(12.);
                ui.label(
                    RichText::new("A SPACE TO THINK OUT LOUD")
                        .size(10.)
                        .color(muted(ui)),
                );
                ui.add_space(10.);
                ui.label(RichText::new("What’s on your mind?").size(34.).strong());
                ui.add_space(6.);
                ui.label(
                    RichText::new("Start with your voice. Pick up wherever you left off.")
                        .color(muted(ui)),
                );
                ui.add_space(20.);
                ui.horizontal(|ui| {
                    if ui
                        .add_enabled(
                            self.connected && !self.starting,
                            egui::Button::new(RichText::new("+ New conversation").strong())
                                .fill(selected(ui)),
                        )
                        .clicked()
                    {
                        self.new_session = Some(String::new());
                    }
                    if let Some(s) = self.sessions.first() {
                        let id = s.id.clone();
                        if ui
                            .add_enabled(!self.starting, egui::Button::new("Continue latest"))
                            .clicked()
                        {
                            self.start_persona.clear();
                            self.begin_chat(id);
                        }
                    }
                });
                ui.add_space(16.);
            });
            ui.add_space(24.);
            ui.label(
                RichText::new("RECENT CONVERSATIONS")
                    .small()
                    .color(muted(ui)),
            );
            if self.sessions.is_empty() {
                ui.label("Your conversations will appear here after your first chat.");
            }
            for s in self.sessions.clone().into_iter().take(3) {
                card(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.vertical(|ui| {
                            ui.strong(if s.summary.is_empty() {
                                "Untitled conversation"
                            } else {
                                &s.summary
                            });
                            ui.label(
                                RichText::new(format!("{} · {}", s.provider, s.updated_at))
                                    .small()
                                    .color(muted(ui)),
                            );
                        });
                        ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                            if ui.button("Resume").clicked() {
                                self.start_persona.clear();
                                self.begin_chat(s.id);
                            }
                        });
                    });
                });
            }
            ui.add_space(20.);
            ui.columns(2, |columns| {
                card(&mut columns[0], |ui| {
                    ui.strong("Meeting");
                    ui.label("Live transcript, notes, bookmarks, and speaker labels.");
                    if ui.button("Set up a recording").clicked() {
                        self.navigate(Screen::Meeting);
                    }
                });
                card(&mut columns[1], |ui| {
                    ui.strong("Personas");
                    ui.label("Choose a voice, model, and identity for the next conversation.");
                    if ui.button("Browse personas").clicked() {
                        self.navigate(Screen::Personas);
                    }
                });
            });
        });
    }
    fn chat(&mut self, ui: &mut egui::Ui) {
        if self.starting {
            ui.horizontal(|ui| {
                ui.spinner();
                ui.label(&self.status);
            });
            ui.label("Models load when you start a conversation.");
            return;
        }
        if !self.active {
            ui.label("Start or resume a conversation from Home.");
            return;
        }
        ui.horizontal_wrapped(|ui| {
            ui.label(RichText::new(&self.state.name).strong().color(accent(ui)));
            ui.label(
                RichText::new(&self.state.session_id)
                    .small()
                    .color(muted(ui)),
            );
            ui.separator();
            ui.selectable_value(&mut self.activity, false, "Chat");
            ui.selectable_value(&mut self.activity, true, "Activity");
            ui.checkbox(&mut self.show_think, "Show thinking");
        });
        ui.horizontal_wrapped(|ui| {
            let mic = if self.state.mic {
                "Pause microphone"
            } else {
                "Resume microphone"
            };
            if ui
                .add_enabled(self.state.voice_available, egui::Button::new(mic))
                .clicked()
            {
                self.action("mic", "", !self.state.mic);
            }
            if ui
                .button(if self.state.muted {
                    "Unmute voice"
                } else {
                    "Mute voice"
                })
                .clicked()
            {
                self.action("audio", "", self.state.muted);
            }
            if ui.button("Interrupt").clicked() {
                self.action("interrupt", "", false);
            }
            if ui
                .button(
                    if self.state.recording == "off" || self.state.recording.is_empty() {
                        "Record talk"
                    } else {
                        "Stop recording"
                    },
                )
                .clicked()
            {
                self.action(
                    "text",
                    if self.state.recording == "off" || self.state.recording.is_empty() {
                        "/record talk"
                    } else {
                        "/record off"
                    },
                    false,
                );
            }
            if ui.button("Compact").clicked() {
                self.action("text", "/compact", false);
            }
        });
        ui.horizontal(|ui| {
            ui.add(
                egui::ProgressBar::new(self.state.input_level.clamp(0., 1.))
                    .desired_width(110.)
                    .text("Mic"),
            );
            ui.add(
                egui::ProgressBar::new(self.state.output_level.clamp(0., 1.))
                    .desired_width(110.)
                    .text("Voice"),
            );
            ui.label(
                RichText::new(&self.state.status).color(if self.state.error {
                    Color32::from_rgb(177, 57, 73)
                } else {
                    muted(ui)
                }),
            );
        });
        let height = (ui.available_height() - 152.).max(100.);
        egui::ScrollArea::vertical()
            .id_salt(if self.activity { "activity" } else { "chat" })
            .stick_to_bottom(true)
            .auto_shrink([false, false])
            .max_height(height)
            .min_scrolled_height(height)
            .show(ui, |ui| {
                ui.set_min_width(ui.available_width());
                if self.activity {
                    if self.state.activity.is_empty() {
                        ui.label("Tool calls, model timing, and work updates appear here.");
                    }
                    for a in &self.state.activity {
                        ui.horizontal_wrapped(|ui| {
                            ui.label(RichText::new(&a.stage).color(accent(ui)));
                            ui.label(&a.detail);
                            if a.milliseconds > 0 {
                                ui.label(
                                    RichText::new(format!("{} ms", a.milliseconds))
                                        .small()
                                        .color(muted(ui)),
                                );
                            }
                        });
                        ui.separator();
                    }
                } else {
                    if self.state.turns.is_empty() {
                        ui.add_space(35.);
                        ui.label(RichText::new("Ready when you are.").size(25.));
                        ui.label(
                            RichText::new("Speak to your agent, hold Talk, or type below.")
                                .color(muted(ui)),
                        );
                    }
                    for turn in &self.state.turns {
                        let user = turn.role == "user";
                        let name = if turn.name.is_empty() {
                            if user { "You" } else { "Assistant" }
                        } else {
                            &turn.name
                        };
                        egui::Frame::new()
                            .fill(if user { selected(ui) } else { card_color(ui) })
                            .inner_margin(16)
                            .corner_radius(18)
                            .show(ui, |ui| {
                                ui.set_min_width((ui.available_width() - 10.).max(0.));
                                ui.label(RichText::new(name).small().strong().color(if user {
                                    muted(ui)
                                } else {
                                    accent(ui)
                                }));
                                if self.show_think && !turn.thinking.is_empty() {
                                    ui.label(
                                        RichText::new(&turn.thinking).italics().color(muted(ui)),
                                    );
                                    ui.separator();
                                }
                                ui.add(egui::Label::new(&turn.content).wrap().selectable(true));
                            });
                    }
                    for result in &self.state.work_results {
                        card(ui, |ui| {
                            ui.strong("Background result");
                            ui.add(egui::Label::new(result).wrap().selectable(true));
                        });
                    }
                    for (i, notice) in self.state.notices.iter().enumerate() {
                        ui.push_id(("notice", i), |ui| {
                            card(ui, |ui| {
                                ui.label(RichText::new(notice).color(muted(ui)));
                            });
                        });
                    }
                    if !self.state.thinking.is_empty() {
                        ui.collapsing("Thinking…", |ui| {
                            ui.label(RichText::new(&self.state.thinking).color(muted(ui)));
                        });
                    }
                    if !self.state.response.is_empty() {
                        card(ui, |ui| {
                            ui.label(RichText::new(&self.state.name).color(accent(ui)));
                            ui.label(&self.state.response);
                        });
                    }
                }
            });
        ui.separator();
        if self.composer.starts_with('/') && !self.composer.contains(' ') {
            ui.horizontal_wrapped(|ui| {
                for command in [
                    "/help",
                    "/settings",
                    "/personas",
                    "/workers",
                    "/work",
                    "/think",
                    "/record",
                    "/speakers",
                    "/compact",
                    "/copy",
                    "/menu",
                ] {
                    if command.starts_with(&self.composer) && ui.small_button(command).clicked() {
                        self.composer = command.into();
                    }
                }
            });
        }
        let composer_id = ui.make_persistent_id("composer");
        let send_key = ui.memory(|m| m.has_focus(composer_id))
            && ui.input_mut(|i| i.consume_key(egui::Modifiers::NONE, egui::Key::Enter));
        let ctx = ui.ctx().clone();
        ui.add(
            egui::TextEdit::multiline(&mut self.composer)
                .desired_rows(2)
                .desired_width(f32::INFINITY)
                .hint_text("Message your agent…  / for commands")
                .id(composer_id),
        );
        ui.horizontal(|ui| {
            let talk = ui.add_enabled(
                self.state.voice_available,
                egui::Button::new(if self.talk_held {
                    "Release to send"
                } else {
                    "Hold to talk"
                })
                .sense(egui::Sense::click_and_drag()),
            );
            if talk.is_pointer_button_down_on() && !self.talk_held {
                self.talk_held = true;
                self.action("talk_down", "", false);
            }
            if self.talk_held && !ui.input(|i| i.pointer.primary_down()) {
                self.talk_held = false;
                self.action("talk_up", "", false);
            }
            ui.label(
                RichText::new("Enter sends · Shift+Enter adds a line · Ctrl+G pauses mic")
                    .small()
                    .color(muted(ui)),
            );
            ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                if ui
                    .add_enabled(
                        !self.composer.trim().is_empty(),
                        egui::Button::new("Send").fill(selected(ui)),
                    )
                    .clicked()
                    || send_key
                {
                    self.submit(&ctx);
                }
            });
        });
    }
    fn session_list(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            ui.add(egui::TextEdit::singleline(&mut self.search).hint_text("Search conversations…"));
            if ui.button("Refresh").clicked() {
                self.refresh();
            }
        });
        egui::ScrollArea::vertical().show(ui, |ui| {
            for session in self.sessions.clone() {
                if !format!("{} {}", session.summary, session.id)
                    .to_lowercase()
                    .contains(&self.search.to_lowercase())
                {
                    continue;
                }
                card(ui, |ui| {
                    ui.strong(if session.summary.is_empty() {
                        "Untitled conversation"
                    } else {
                        &session.summary
                    });
                    ui.label(
                        RichText::new(format!(
                            "{} · {} · {} turns",
                            session.updated_at,
                            session.model,
                            session.turns.as_ref().map_or(0, Vec::len)
                        ))
                        .color(muted(ui)),
                    );
                    ui.horizontal(|ui| {
                        if ui.button("Resume").clicked() {
                            self.start_persona.clear();
                            self.begin_chat(session.id.clone());
                        }
                        if ui.button("Delete…").clicked() {
                            self.confirm = Some((
                                format!("Delete conversation {}?", session.id),
                                vec!["@session_delete".into(), session.id.clone()],
                            ));
                        }
                    });
                });
            }
        });
    }
    fn persona_list(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            ui.label(
                RichText::new(if self.active {
                    "Choose the responder for the next turn. Your conversation stays open."
                } else {
                    "Select a default, or create an agent with its own prompt, model, and voice."
                })
                .color(muted(ui)),
            );
        });
        ui.horizontal(|ui| {
            if ui.button("+ Create persona").clicked() {
                self.persona_form = PersonaForm {
                    open: true,
                    prompt: "You are {agent_name}, a voice assistant.".into(),
                    ..Default::default()
                };
            }
            if ui.button("Refresh").clicked() {
                self.command(&["persona", "list", "--json"], Purpose::Personas);
            }
        });
        egui::ScrollArea::vertical().show(ui, |ui| {
            for p in self.personas.clone() {
                card(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.label(RichText::new(&p.display_name).size(20.).strong());
                        if p.active {
                            ui.label(RichText::new("DEFAULT").small().color(accent(ui)));
                        }
                        if p.inactive {
                            ui.label("Inactive");
                        }
                    });
                    ui.label(
                        RichText::new(format!("{} · {} · {}", p.id, p.provider, p.voice))
                            .color(muted(ui)),
                    );
                    ui.horizontal_wrapped(|ui| {
                        if ui
                            .add_enabled(
                                !p.inactive,
                                egui::Button::new(if self.active {
                                    "Use in this chat"
                                } else {
                                    "Make default"
                                }),
                            )
                            .clicked()
                        {
                            if self.active {
                                self.action("persona", &p.id, false);
                            } else {
                                self.cli(
                                    vec![
                                        "persona".into(),
                                        "use".into(),
                                        p.id.clone(),
                                        "--json".into(),
                                    ],
                                    Purpose::Mutation("Default persona saved".into()),
                                    false,
                                );
                            }
                        }
                        if ui.button("Edit").clicked() {
                            self.cli(
                                vec![
                                    "persona".into(),
                                    "show".into(),
                                    p.id.clone(),
                                    "--with-prompt".into(),
                                    "--json".into(),
                                ],
                                Purpose::PersonaLoad,
                                false,
                            );
                        }
                        if ui
                            .add_enabled(
                                !p.active,
                                egui::Button::new(if p.inactive { "Enable" } else { "Disable" }),
                            )
                            .clicked()
                        {
                            self.cli(
                                vec![
                                    "persona".into(),
                                    if p.inactive { "enable" } else { "disable" }.into(),
                                    p.id.clone(),
                                    "--json".into(),
                                ],
                                Purpose::Mutation("Persona availability saved".into()),
                                false,
                            );
                        }
                        if ui
                            .add_enabled(!p.builtin, egui::Button::new("Delete…"))
                            .clicked()
                        {
                            self.confirm = Some((
                                format!("Delete persona {} and its owned prompt?", p.display_name),
                                vec![
                                    "persona".into(),
                                    "delete".into(),
                                    p.id.clone(),
                                    "--yes".into(),
                                    "--json".into(),
                                ],
                            ));
                        }
                    });
                });
            }
        });
    }
    fn workers(&mut self, ui: &mut egui::Ui) {
        ui.collapsing("Worker models, limits, and spending policy", |ui| {
            ui.label("Assignments and limits apply through the shared Obey Voice policy. Saving checks for concurrent edits.");
            egui::ScrollArea::vertical().id_salt("worker_policy").max_height(360.).show(ui, |ui| {
                edit_policy(ui, "Workers", &mut self.worker_config);
            });
            ui.horizontal(|ui| {
                if ui.add_enabled(!self.worker_config.is_null(), egui::Button::new("Save worker policy")).clicked() {
                    self.request("worker_config_save", json!({"config":self.worker_config,"revision":self.worker_revision}), Purpose::WorkerConfig);
                }
                if ui.button("Reload policy").clicked() { self.request("worker_config_get", json!({}), Purpose::WorkerConfig); }
            });
        });
        if !self.active {
            ui.label("Worker controls become available inside a conversation. Configure workers in Settings.");
            if ui.button("Open settings").clicked() {
                self.navigate(Screen::Settings);
            }
            return;
        }
        let mut enabled = self.state.workers_enabled;
        ui.horizontal(|ui| {
            if ui
                .checkbox(&mut enabled, "Allow new background workers")
                .changed()
            {
                self.action("workers_enabled", "", enabled);
            }
            if ui.button("Stop all…").clicked() {
                self.confirm = Some((
                    "Stop every running and queued task?".into(),
                    vec!["@workers_stop".into()],
                ));
            }
        });
        ui.label(
            RichText::new(
                "Turning workers off affects new tasks. Existing tasks continue until stopped.",
            )
            .small()
            .color(muted(ui)),
        );
        egui::ScrollArea::vertical().show(ui, |ui| {
            if self.state.workers.is_empty() {
                ui.label("No background tasks in this conversation.");
            }
            for w in self.state.workers.clone() {
                let id = string(&w, "ID");
                card(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.strong(string(&w, "Task"));
                        ui.label(RichText::new(string(&w, "State")).color(accent(ui)));
                    });
                    ui.label(
                        RichText::new(format!(
                            "{} · {}",
                            string(&w, "Provider"),
                            string(&w, "Model")
                        ))
                        .color(muted(ui)),
                    );
                    ui.label(string(&w, "Activity"));
                    ui.horizontal(|ui| {
                        if ui.button("Details").clicked() {
                            self.selected_worker = Some(id.clone());
                        }
                        if ui.button("Stop task").clicked() {
                            self.action("worker_stop", &id, false);
                        }
                        for action in ["dismiss", "speak", "replay"] {
                            if ui.small_button(action).clicked() {
                                self.action("text", &format!("/work {action} {id}"), false);
                            }
                        }
                    });
                    if self.selected_worker.as_ref() == Some(&id) {
                        ui.separator();
                        ui.label(string(&w, "Result"));
                        ui.colored_label(Color32::from_rgb(177, 57, 73), string(&w, "Err"));
                        ui.label(string(&w, "Termination"));
                    }
                });
            }
        });
    }
    fn settings(&mut self, ui: &mut egui::Ui) {
        ui.horizontal_wrapped(|ui| {
            for group in [
                "voice", "brain", "speakers", "meetings", "tools", "remote", "advanced", "models",
            ] {
                ui.selectable_value(&mut self.group, group.into(), group);
            }
            let mut extra: Vec<_> = self
                .schema
                .iter()
                .map(|s| s.group.as_str())
                .filter(|g| {
                    ![
                        "hidden", "voice", "brain", "speakers", "meetings", "tools", "remote",
                        "advanced", "models",
                    ]
                    .contains(g)
                })
                .collect();
            extra.sort_unstable();
            extra.dedup();
            for group in extra {
                ui.selectable_value(&mut self.group, group.into(), group);
            }
        });
        ui.horizontal(|ui| {
            ui.add(egui::TextEdit::singleline(&mut self.search).hint_text("Find a setting…"));
            if ui.button("Reload").clicked() {
                self.command(&["config", "get", "--json"], Purpose::SettingsValues);
            }
        });
        if !self.settings_persona.is_null() {
            ui.collapsing("Active persona overrides", |ui| {
                ui.label(
                    "Persona model and voice choices can override these application defaults.",
                );
                ui.monospace(
                    serde_json::to_string_pretty(&self.settings_persona).unwrap_or_default(),
                );
            });
        }
        if self.group == "models" {
            ui.horizontal(|ui| {
                if ui.button("Check models").clicked() {
                    self.start_task(
                        "Model status",
                        vec!["models".into(), "status".into(), "--json".into()],
                    );
                }
                if ui.button("Install required models").clicked() {
                    self.start_task(
                        "Install models",
                        vec!["models".into(), "ensure".into(), "--json".into()],
                    );
                }
                if ui.button("Run diagnostics").clicked() {
                    self.start_task("Diagnostics", vec!["doctor".into()]);
                }
            });
        }
        let keys: Vec<_> = self
            .schema
            .iter()
            .filter(|s| {
                s.group != "hidden"
                    && if self.search.is_empty() {
                        s.group == self.group
                    } else {
                        format!("{} {} {}", s.key, s.title, s.help)
                            .to_lowercase()
                            .contains(&self.search.to_lowercase())
                    }
            })
            .cloned()
            .collect();
        egui::ScrollArea::vertical()
            .id_salt("settings_scroll")
            .show(ui, |ui| {
                if self.group == "speakers" && self.search.is_empty() {
                    card(ui, |ui| {
                        ui.strong("Voice registration");
                        if !self.active { ui.label("Open a voice conversation, then return here to register a voice using its microphone."); }
                        field(ui, "Speaker name", &mut self.enroll_name);
                        ui.checkbox(&mut self.enroll_owner, "Register as your voice (owner)");
                        if self.state.enrollment.active {
                            let e = &self.state.enrollment;
                            ui.label(if e.processing { format!("Learning {}’s voice…", e.name) } else { format!("Recording {} · clip {} of 3 · {:.1} seconds", e.name, e.clip, e.seconds) });
                            if ui.button("Cancel registration").clicked() { self.action("enroll_cancel", "", false); }
                        } else if ui.add_enabled(self.active && self.state.voice_available && !self.enroll_name.trim().is_empty(), egui::Button::new("Record voice samples")).clicked() {
                            self.action("enroll", &self.enroll_name.clone(), self.enroll_owner);
                        }
                        for name in self.state.speaker_names.clone() { ui.horizontal(|ui| {
                            ui.label(&name);
                            if ui.small_button("Remove registration…").clicked() { self.confirm = Some((format!("Remove the voice registration for {name}?"), vec!["@enroll_remove".into(),name])); }
                        }); }
                        ui.label(&self.state.settings_message);
                    });
                }
                if self.group == "voice" && self.active && self.search.is_empty() {
                    ui.horizontal(|ui| {
                        if ui.button("Preview saved voice").clicked() {
                            let voice = self.values["voice"]["value"].as_str().unwrap_or_default().to_string();
                            self.action("preview", &voice, false);
                        }
                        if ui.button("Stop preview").clicked() { self.action("preview_stop", "", false); }
                        ui.label(&self.state.settings_message);
                    });
                }
                for spec in keys {
                    ui.push_id(&spec.key, |ui| {
                        card(ui, |ui| {
                            ui.horizontal(|ui| {
                                ui.strong(&spec.title);
                                if spec.restart_required {
                                    ui.label(
                                        RichText::new("Next conversation").small().color(muted(ui)),
                                    );
                                }
                            });
                            ui.label(RichText::new(&spec.help).small().color(muted(ui)));
                            let current = self.values[&spec.key]["value"].clone();
                            let source = string(&self.values[&spec.key], "source");
                            ui.label(
                                RichText::new(format!("{} · {}", spec.key, source))
                                    .monospace()
                                    .small()
                                    .color(muted(ui)),
                            );
                            if !spec.editable {
                                ui.label(format!("Managed by {}", spec.managed_by));
                                return;
                            }
                            let value = self.drafts.entry(spec.key.clone()).or_insert_with(|| {
                                display(if current.is_null() {
                                    &spec.default
                                } else {
                                    &current
                                })
                            });
                            match spec.kind.as_str() {
                                "bool" => {
                                    let mut on = value == "true";
                                    if ui.checkbox(&mut on, "Enabled").changed() {
                                        *value = on.to_string();
                                    }
                                }
                                "enum" => {
                                    egui::ComboBox::from_id_salt("value")
                                        .selected_text(if value.is_empty() {
                                            "App default"
                                        } else {
                                            value.as_str()
                                        })
                                        .show_ui(ui, |ui| {
                                            if spec.allows_empty {
                                                ui.selectable_value(
                                                    value,
                                                    String::new(),
                                                    "App default",
                                                );
                                            }
                                            for choice in &spec.choices {
                                                ui.selectable_value(value, choice.clone(), choice);
                                            }
                                        });
                                }
                                _ => {
                                    ui.add(
                                        egui::TextEdit::singleline(value)
                                            .password(spec.secret)
                                            .desired_width(f32::INFINITY),
                                    );
                                }
                            }
                            let saved = display(if current.is_null() {
                                &spec.default
                            } else {
                                &current
                            });
                            let changed = *value != saved;
                            let draft = value.clone();
                            ui.horizontal(|ui| {
                                if ui.add_enabled(changed, egui::Button::new("Save")).clicked() {
                                    self.cli(
                                        vec![
                                            "config".into(),
                                            "set".into(),
                                            spec.key.clone(),
                                            draft,
                                            "--json".into(),
                                        ],
                                        Purpose::SettingSave(spec.key.clone()),
                                        false,
                                    );
                                }
                                if ui.button("Reset to default").clicked() {
                                    self.cli(
                                        vec![
                                            "config".into(),
                                            "unset".into(),
                                            spec.key.clone(),
                                            "--json".into(),
                                        ],
                                        Purpose::SettingSave(spec.key.clone()),
                                        false,
                                    );
                                }
                            });
                        });
                    });
                }
                if self.group == "advanced" {
                    ui.collapsing("Engine diagnostics", |ui| {
                        for line in &self.engine_logs {
                            ui.monospace(line);
                        }
                    });
                }
                self.task_panel(ui);
            });
    }
    fn library_screen(&mut self, ui: &mut egui::Ui) {
        ui.label(
            RichText::new("Browse your Calibre library and send a book to audiobook creation.")
                .color(muted(ui)),
        );
        ui.horizontal(|ui| {
            let edit = ui.add(
                egui::TextEdit::singleline(&mut self.library_query)
                    .hint_text("Title, author, or Calibre query…"),
            );
            if ui.button("Search").clicked()
                || (edit.lost_focus() && ui.input(|i| i.key_pressed(egui::Key::Enter)))
            {
                self.load_library();
            }
        });
        egui::ScrollArea::vertical().show(ui, |ui| {
            for book in self.library.clone() {
                card(ui, |ui| {
                    ui.strong(string(&book, "title"));
                    ui.label(display(&book["authors"]));
                    ui.label(
                        RichText::new(display(&book["formats"]))
                            .small()
                            .color(muted(ui)),
                    );
                    ui.horizontal(|ui| {
                        let id = display(&book["id"]);
                        if ui.button("Details").clicked() {
                            self.cli(
                                vec!["library".into(), "show".into(), id.clone(), "--json".into()],
                                Purpose::Book,
                                false,
                            );
                        }
                        if ui.button("Create audiobook").clicked() {
                            self.input = format!("id:{id}");
                            self.from_library = true;
                            self.navigate(Screen::Audiobook);
                        }
                    });
                });
            }
            if !self.book.is_null() {
                ui.separator();
                ui.monospace(serde_json::to_string_pretty(&self.book).unwrap_or_default());
            }
        });
    }
    fn audiobook(&mut self, ui: &mut egui::Ui) {
        egui::ScrollArea::vertical().show(ui, |ui| {
            ui.label(
                RichText::new("Turn a document, ebook, or URL into narrated chapter files.")
                    .color(muted(ui)),
            );
            card(ui, |ui| {
                field(
                    ui,
                    if self.from_library {
                        "Calibre query"
                    } else {
                        "Input file or URL"
                    },
                    &mut self.input,
                );
                ui.checkbox(&mut self.from_library, "Resolve input from Calibre");
                field(ui, "Output directory", &mut self.out_dir);
                ui.columns(2, |cols| {
                    field(&mut cols[0], "Voice (empty uses settings)", &mut self.voice);
                    field(
                        &mut cols[1],
                        "Language (empty uses settings)",
                        &mut self.language,
                    );
                });
                ui.horizontal(|ui| {
                    ui.label("Speed");
                    ui.add(
                        egui::TextEdit::singleline(&mut self.speed)
                            .desired_width(65.)
                            .hint_text("Default"),
                    );
                    ui.label("Audio format");
                    combo(
                        ui,
                        "audio_format",
                        &mut self.format,
                        &["", "mp3", "m4a", "m4b", "aac", "opus"],
                    );
                });
                ui.checkbox(
                    &mut self.resume_render,
                    "Resume completed chapters from the manifest",
                );
                if ui
                    .add_enabled(
                        self.task.is_none()
                            && !self.input.trim().is_empty()
                            && !self.out_dir.trim().is_empty(),
                        egui::Button::new("Create audiobook").fill(selected(ui)),
                    )
                    .clicked()
                {
                    let mut args = vec!["render".into()];
                    if self.from_library {
                        args.push("--from-library".into());
                    }
                    args.push(self.input.clone());
                    args.extend(["--out-dir".into(), self.out_dir.clone(), "--json".into()]);
                    for (flag, value) in [
                        ("--voice", &self.voice),
                        ("--language", &self.language),
                        ("--speed", &self.speed),
                        ("--audio-format", &self.format),
                    ] {
                        if !value.is_empty() {
                            args.extend([flag.into(), value.clone()]);
                        }
                    }
                    if self.resume_render {
                        args.push("--resume".into());
                    }
                    self.start_task("Create audiobook", args);
                }
            });
            self.task_panel(ui);
        });
    }
    fn remote_screen(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.label(RichText::new("Bring your agent to another device.").size(24.));
            ui.label(RichText::new("Start sharing, open the address on your other device, and enter the pairing code.").color(muted(ui)));
            ui.horizontal(|ui| {
                ui.selectable_value(&mut self.tailscale, true, "Tailscale");
                ui.selectable_value(&mut self.tailscale, false, "Same Wi-Fi");
            });
            if self.remote.is_none() {
                if ui.button("Start sharing").clicked() {
                    self.logs.clear();
                    let mut args = vec!["serve".into(), "--banner-json".into()];
                    if self.tailscale {
                        args.push("--tailscale".into());
                    }
                    let id = self.cli(args, Purpose::Task, true);
                    if !self.demo {
                        self.remote = Some(id);
                    }
                    self.status = "Starting remote access…".into();
                }
            } else {
                ui.horizontal(|ui| {
                    if ui.button("Stop sharing").clicked() {
                        self.stop_remote();
                    }
                    ui.label("Sharing stops when you leave this screen.");
                });
            }
            if !self.remote_url.is_empty() {
                ui.add_space(18.);
                ui.label(RichText::new(&self.remote_url).size(20.).color(accent(ui)));
                if ui.button("Copy address").clicked() {
                    ui.ctx().copy_text(self.remote_url.clone());
                }
            }
            if !self.pairing.is_empty() {
                ui.label("PAIRING CODE");
                ui.label(RichText::new(&self.pairing).size(36.).monospace());
                if ui.button("Copy code").clicked() {
                    ui.ctx().copy_text(self.pairing.clone());
                }
            }
        });
        ui.collapsing("Server status", |ui| {
            egui::ScrollArea::vertical()
                .max_height(250.)
                .show(ui, |ui| {
                    for line in &self.logs {
                        if let Ok(v) = serde_json::from_str::<Value>(line)
                            && v.get("token").is_some()
                        {
                            continue;
                        }
                        ui.monospace(line);
                    }
                });
        });
    }
    fn meeting(&mut self, ui: &mut egui::Ui) {
        egui::ScrollArea::vertical().show(ui, |ui| {
            if !self.meeting_active {
                card(ui, |ui| {
                    field(ui, "Meeting title", &mut self.meeting_title);
                    ui.horizontal(|ui| {
                        if ui
                            .add_enabled(
                                !self.starting && !self.meeting_title.trim().is_empty(),
                                egui::Button::new("Start recording"),
                            )
                            .clicked()
                        {
                            self.meeting_lines.clear();
                            self.meeting_partial.clear();
                            self.meeting_notes.clear();
                            self.meeting_level = 0.;
                            self.meeting_phase = "Preparing microphone…".into();
                            self.meeting_result = Value::Null;
                            self.starting = true;
                            self.request(
                                "meeting_start",
                                json!({"text":self.meeting_title}),
                                Purpose::MeetingStart,
                            );
                            if self.demo {
                                self.starting = false;
                                self.meeting_active = true;
                                self.meeting_phase = "Preview · no microphone".into();
                                self.meeting_lines = vec!["00:04  Let’s capture decisions as we go.".into()];
                                self.meeting_partial = "We should start with…".into();
                            }
                        }
                        if ui
                            .add_enabled(!self.starting, egui::Button::new("Resume last meeting"))
                            .clicked()
                        {
                            self.starting = true;
                            self.request(
                                "meeting_start",
                                json!({"resume":"last"}),
                                Purpose::MeetingStart,
                            );
                        }
                    });
                });
            } else {
                card(ui, |ui| {
                    ui.label(RichText::new(if self.meeting_title.is_empty() { "Meeting" } else { &self.meeting_title }).size(22.));
                    ui.horizontal(|ui| {
                        ui.label(RichText::new(if self.starting { "Saving meeting…" } else if self.meeting_paused { "Paused" } else { "● Recording" }).color(accent(ui)));
                        if !self.meeting_paused { ui.label(&self.meeting_phase); }
                        ui.add(egui::ProgressBar::new(self.meeting_level.clamp(0., 1.)).desired_width(100.).text("Mic"));
                    });
                    ui.add_enabled_ui(!self.starting, |ui| {
                        ui.horizontal(|ui| {
                            if ui.button(if self.meeting_paused { "Resume" } else { "Pause" }).clicked() {
                                self.request(if self.meeting_paused { "meeting_resume" } else { "meeting_pause" }, json!({}), Purpose::MeetingPaused(!self.meeting_paused));
                                if self.demo { self.meeting_paused = !self.meeting_paused; }
                            }
                            if ui.button("★ Bookmark").clicked() {
                                self.request("meeting_bookmark", json!({"text":self.meeting_note}), Purpose::MeetingNote(format!("★ {}", self.meeting_note)));
                            }
                            if ui.button("Stop and review").clicked() {
                                self.starting = !self.demo;
                                self.request("meeting_stop", json!({}), Purpose::MeetingStop);
                                if self.demo { self.meeting_active = false; }
                            }
                        });
                    });
                });
                ui.add_space(12.);
                ui.columns(2, |cols| {
                    card(&mut cols[0], |ui| {
                        ui.strong("Live transcript");
                        egui::ScrollArea::vertical().id_salt("live_meeting_transcript").max_height(340.).stick_to_bottom(true).show(ui, |ui| {
                            ui.set_min_height(180.);
                            if self.meeting_lines.is_empty() && self.meeting_partial.is_empty() { ui.label(RichText::new("Listening. Your transcript will appear here as you speak.").color(muted(ui))); }
                            for line in &self.meeting_lines { ui.add(egui::Label::new(line).wrap().selectable(true)); }
                            if !self.meeting_partial.is_empty() { ui.label(RichText::new(&self.meeting_partial).italics().color(muted(ui))); }
                        });
                    });
                    card(&mut cols[1], |ui| {
                        ui.strong("Your notes");
                        ui.label(RichText::new("Capture decisions, questions, and follow-ups.").small().color(muted(ui)));
                        ui.add(egui::TextEdit::multiline(&mut self.meeting_note).hint_text("Add a timestamped note…").desired_rows(4).desired_width(f32::INFINITY));
                        let pending = self.pending.values().any(|p| matches!(p, Purpose::MeetingNote(_)));
                        if ui.add_enabled(!self.starting && !pending && !self.meeting_note.trim().is_empty(), egui::Button::new("Save note")).clicked() {
                            let note = self.meeting_note.clone();
                            self.request("meeting_note", json!({"text":note}), Purpose::MeetingNote(note.clone()));
                            if self.demo { self.meeting_notes.push(note); self.meeting_note.clear(); }
                        }
                        egui::ScrollArea::vertical().id_salt("live_meeting_notes").max_height(180.).stick_to_bottom(true).show(ui, |ui| {
                            for note in &self.meeting_notes { ui.add(egui::Label::new(format!("✓ {note}")).wrap().selectable(true)); }
                        });
                    });
                });
            }
            if self.meeting_active { return; }
            if !self.meeting_result.is_null() {
                card(ui, |ui| {
                    ui.strong("Meeting saved");
                    if let Some(document) = self.meeting_result["document"].as_str() {
                        ui.add(egui::Label::new(document).wrap().selectable(true));
                    } else {
                        ui.label(&self.meeting_path);
                        if ui.button("Read transcript").clicked() {
                            self.cli(
                                vec![
                                    "meeting".into(),
                                    "show".into(),
                                    self.meeting_path.clone(),
                                    "--json".into(),
                                ],
                                Purpose::MeetingDocument,
                                false,
                            );
                        }
                    }
                    ui.label("File notes to a campaign");
                    ui.horizontal(|ui| {
                        egui::ComboBox::from_id_salt("meeting_campaign")
                            .selected_text(if self.route_to.is_empty() { "Choose a destination" } else { &self.route_to })
                            .show_ui(ui, |ui| {
                                for campaign in &self.campaigns {
                                    let name = string(campaign, "name");
                                    if !name.is_empty() { ui.selectable_value(&mut self.route_to, format!("camp:{name}"), name); }
                                }
                            });
                        if ui.button("Refresh destinations").clicked() { self.command(&["--campaigns"], Purpose::Campaigns); }
                    });
                    if let Some(error) = &self.campaign_error { ui.label(RichText::new(format!("Campaign discovery: {error}")).color(Color32::LIGHT_RED)); }
                    ui.collapsing("Other destination", |ui| { field(ui, "Destination ID from your routing config", &mut self.route_to); });
                    if ui.add_enabled(!self.route_to.trim().is_empty() && self.task.is_none(), egui::Button::new("File meeting notes")).clicked() {
                        self.logs.clear();
                        self.task_title = "Route meeting".into();
                        let id = self.cli(vec!["meeting".into(), "route".into(), self.meeting_path.clone(), "--to".into(), self.route_to.clone(), "--json".into()], Purpose::MeetingRoute, false);
                        if !self.demo { self.task = Some(id); }
                        self.status = "Filing meeting notes…".into();
                    }
                });
            }
            ui.add_space(12.);
            ui.strong("Meeting history");
            for meeting in self.meetings.clone() {
                card(ui, |ui| {
                    ui.strong(string(&meeting, "description"));
                    ui.label(string(&meeting, "started_at"));
                    if ui.button("Review").clicked() {
                        self.meeting_path = string(&meeting, "bundle");
                        let id = string(&meeting, "id");
                        self.cli(
                            vec!["meeting".into(), "show".into(), id, "--json".into()],
                            Purpose::MeetingDocument,
                            false,
                        );
                    }
                });
            }
            self.task_panel(ui);
        });
    }
    fn task_panel(&mut self, ui: &mut egui::Ui) {
        if self.logs.is_empty() && self.task.is_none() {
            return;
        }
        ui.separator();
        ui.horizontal(|ui| {
            ui.strong(&self.task_title);
            if let Some(id) = self.task {
                ui.spinner();
                if ui.button("Cancel").clicked() {
                    self.backend.cancel(id);
                    self.status = "Cancel requested…".into();
                }
            }
        });
        egui::ScrollArea::vertical()
            .id_salt("task_log")
            .max_height(250.)
            .stick_to_bottom(true)
            .show(ui, |ui| {
                for line in &self.logs {
                    ui.add(egui::Label::new(RichText::new(line).monospace().small()).wrap());
                }
            });
    }
    pub fn diagnostics_dialog(&mut self, ctx: &egui::Context) {
        if !self.diagnostics_open {
            return;
        }
        let snapshot = crate::diagnostics::snapshot();
        let modal = egui::Modal::new(egui::Id::unique("diagnostics"))
            .frame(crate::theme::dialog(self.dark))
            .show(ctx, |ui| {
                ui.set_width((ctx.content_rect().width() - 100.).clamp(300., 820.));
                ui.heading("Diagnostics");
                ui.label("Desktop events, engine output, and command failures are saved locally.");
                ui.add(
                    egui::Label::new(RichText::new(snapshot.path.display().to_string()).small())
                        .selectable(true),
                );
                if let Some(error) = &snapshot.error {
                    ui.colored_label(Color32::LIGHT_RED, format!("Log file unavailable: {error}"));
                }
                ui.horizontal(|ui| {
                    if ui.button("Copy log path").clicked() {
                        ctx.copy_text(snapshot.path.display().to_string());
                    }
                    if ui.button("Copy visible logs").clicked() {
                        ctx.copy_text(
                            snapshot
                                .lines
                                .iter()
                                .filter(|l| l.contains(&self.diagnostics_filter))
                                .cloned()
                                .collect::<Vec<_>>()
                                .join("\n"),
                        );
                    }
                    if ui.button("Close").clicked() {
                        self.diagnostics_open = false;
                    }
                });
                ui.add(
                    egui::TextEdit::singleline(&mut self.diagnostics_filter)
                        .hint_text("Filter by error, event, or request ID…")
                        .desired_width(f32::INFINITY),
                );
                egui::ScrollArea::vertical()
                    .max_height((ctx.content_rect().height() - 300.).max(150.))
                    .stick_to_bottom(true)
                    .show(ui, |ui| {
                        for line in &snapshot.lines {
                            if line.contains(&self.diagnostics_filter) {
                                ui.add(
                                    egui::Label::new(RichText::new(line).monospace().size(11.))
                                        .wrap()
                                        .selectable(true),
                                );
                            }
                        }
                    });
            });
        if modal.should_close() {
            self.diagnostics_open = false;
        }
        ctx.request_repaint_after(std::time::Duration::from_millis(250));
    }

    pub fn dialogs(&mut self, ctx: &egui::Context) {
        if let Some(session) = self.new_session.clone() {
            let modal = egui::Modal::new(egui::Id::unique("start_conversation"))
                .frame(crate::theme::dialog(self.dark))
                .backdrop_color(Color32::from_black_alpha(85))
                .show(ctx, |ui| {
                    ui.set_width(420.);
                    ui.label(
                        RichText::new("NEW CONVERSATION")
                            .size(11.)
                            .color(accent(ui)),
                    );
                    ui.heading("Start a conversation");
                    ui.label(
                        RichText::new("Choose a persona, then make yourself heard.")
                            .color(muted(ui)),
                    );
                    ui.add_space(12.);
                    ui.label(RichText::new("Persona").strong());
                    egui::ComboBox::from_id_salt("start_persona")
                        .width(ui.available_width())
                        .selected_text(if self.start_persona.is_empty() {
                            "Saved default"
                        } else {
                            &self.start_persona
                        })
                        .show_ui(ui, |ui| {
                            ui.selectable_value(
                                &mut self.start_persona,
                                String::new(),
                                "Saved default",
                            );
                            ui.selectable_value(
                                &mut self.start_persona,
                                ":defaults".into(),
                                "Application defaults",
                            );
                            for p in &self.personas {
                                if !p.inactive {
                                    ui.selectable_value(
                                        &mut self.start_persona,
                                        p.id.clone(),
                                        &p.display_name,
                                    );
                                }
                            }
                        });
                    ui.add_space(12.);
                    ui.label(RichText::new("Voice & audio").strong());
                    let mut microphone = !self.text_only;
                    let mut spoken_replies = !self.silent;
                    ui.checkbox(&mut microphone, "Microphone input")
                        .on_hover_text("Turn off to type your messages instead.");
                    ui.checkbox(&mut spoken_replies, "Spoken replies")
                        .on_hover_text("Turn off to read replies without audio.");
                    self.text_only = !microphone;
                    self.silent = !spoken_replies;
                    ui.add_space(12.);
                    ui.horizontal(|ui| {
                        if ui
                            .add_sized(
                                [260., 42.],
                                egui::Button::new(
                                    RichText::new("Start conversation").color(Color32::WHITE),
                                )
                                .fill(Color32::from_rgb(66, 91, 199)),
                            )
                            .clicked()
                        {
                            self.new_session = None;
                            self.begin_chat(session);
                        }
                        if ui
                            .add_sized([140., 42.], egui::Button::new("Cancel"))
                            .clicked()
                        {
                            self.new_session = None;
                        }
                    });
                });
            if modal.should_close() {
                self.new_session = None;
            }
        }
        if self.persona_form.open {
            let title = if self.persona_form.id.is_empty() {
                "Create persona"
            } else {
                "Edit persona"
            };
            egui::Window::new(title).default_width(520.).resizable(true).show(ctx, |ui| {
                let f = &mut self.persona_form;
                field(ui, "Display name", &mut f.name);
                ui.columns(2, |cols| {
                    cols[0].label("Brain");
                    combo(&mut cols[0], "brain", &mut f.brain, &["", "ollama", "claude", "cursor", "grok"]);
                    field(&mut cols[1], "Model", &mut f.model);
                });
                ui.columns(2, |cols| {
                    cols[0].label("Speech provider");
                    combo(&mut cols[0], "tts", &mut f.tts, &["", "kokoro", "qwen3-tts"]);
                    field(&mut cols[1], "Voice", &mut f.voice);
                });
                ui.horizontal(|ui| {
                    ui.label("Qwen tier");
                    combo(ui, "tier", &mut f.tier, &["", "0.6b", "1.7b"]);
                });
                if f.structured {
                    ui.label("This prompt has structured sections. Edit its source document to preserve that structure.");
                } else {
                    ui.label("System prompt");
                    ui.add(egui::TextEdit::multiline(&mut f.prompt).desired_rows(8).desired_width(f32::INFINITY));
                }
                ui.horizontal(|ui| {
                    if ui.add_enabled(!self.persona_form.name.trim().is_empty(), egui::Button::new("Save persona")).clicked() { self.save_persona(); }
                    if ui.button("Cancel").clicked() { self.persona_form.open = false; }
                });
            });
        }
        if let Some((message, args)) = self.confirm.clone() {
            egui::Window::new("Confirm")
                .collapsible(false)
                .resizable(false)
                .show(ctx, |ui| {
                    ui.label(message);
                    ui.horizontal(|ui| {
                        if ui.button("Confirm").clicked() {
                            self.confirm = None;
                            if args[0] == "@session_delete" {
                                self.request(
                                    "session_delete",
                                    json!({"session_id":args[1]}),
                                    Purpose::Mutation("Conversation deleted".into()),
                                );
                            } else if args[0] == "@enroll_remove" {
                                self.action("enroll_remove", &args[1], false);
                            } else if args[0] == "@workers_stop" {
                                self.action("workers_stop", "", false);
                            } else {
                                self.cli(args, Purpose::Mutation("Deleted".into()), false);
                            }
                        }
                        if ui.button("Cancel").clicked() {
                            self.confirm = None;
                        }
                    });
                });
        }
    }
}

// Native controls for the versioned upstream policy; upstream remains the
// validator, revision authority, and store.
fn edit_policy(ui: &mut egui::Ui, key: &str, value: &mut Value) {
    ui.push_id(key, |ui| {
        match value {
            Value::Object(object) => { ui.collapsing(key.replace('_', " "), |ui| {
                for (name, value) in object.iter_mut() {
                    if name != "revision" && name != "schema_version" { edit_policy(ui, name, value); }
                }
            }); },
            Value::Bool(v) => { ui.checkbox(v, key.replace('_', " ")); },
            Value::Number(v) => { ui.horizontal(|ui| { ui.label(key.replace('_', " ")); let mut n = v.as_i64().unwrap_or(0); if ui.add(egui::DragValue::new(&mut n).range(0..=i64::MAX)).changed() { *v = n.into(); } }); },
            Value::String(v) => { field(ui, &key.replace('_', " "), v); },
            Value::Null if ui.button(format!("Set {}", key.replace('_', " "))).clicked() => {
                *value = if key == "assignment" { json!({"provider":"ollama","model":"","endpoint_ref":null,"account_ref":null,"billing":"local"}) }
                    else if key.ends_with("_ref") { json!("") } else { json!(0) };
            },
            _ => {}
        }
        if !value.is_null() && ["assignment","endpoint_ref","account_ref","output_tokens","charge_micro_usd","daily_charge_micro_usd"].contains(&key) && ui.small_button("Unset").clicked() { *value = Value::Null; }
    });
}
