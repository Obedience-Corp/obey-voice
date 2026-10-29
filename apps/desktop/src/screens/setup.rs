mod audio;
mod choices;

use super::card;
use crate::app::VoiceApp;
use crate::model::Purpose;
use crate::setup::Operation;
use crate::theme::{muted, selected};
use eframe::egui::{self, RichText};
use serde_json::json;

const STEPS: [&str; 5] = ["Welcome", "Brain", "Speech", "Audio", "Try it"];

impl VoiceApp {
    pub(super) fn setup_screen(&mut self, ui: &mut egui::Ui) {
        ui.horizontal_wrapped(|ui| {
            for (index, title) in STEPS.iter().enumerate() {
                let current = index == self.setup.progress.step;
                ui.label(
                    RichText::new(format!("{}  {}", index + 1, title)).color(if current {
                        ui.visuals().text_color()
                    } else {
                        muted(ui)
                    }),
                );
                if index < 4 {
                    ui.label("›");
                }
            }
        });
        ui.add_space(12.);
        if !self.connected {
            card(ui, |ui| {
                ui.heading("Let’s connect to the voice engine");
                ui.label("The app needs its companion engine to read your setup. Reconnect in the sidebar, or open Diagnostics for details.");
            });
            return;
        }
        if !self.setup.loaded {
            ui.label("Reading your setup…");
            if let Some(error) = &self.setup.error {
                ui.label(error);
            }
            if self.setup.busy.is_none() && ui.button("Retry setup discovery").clicked() {
                self.setup_request("setup_status", json!({}), Operation::Load);
            }
            return;
        }
        let saving = self.pending.values().any(|p| {
            matches!(
                p,
                Purpose::SettingSave(_) | Purpose::SettingsValues | Purpose::SettingsSchema
            )
        });
        let busy = self.setup.busy.is_some() || saving || self.starting;
        egui::ScrollArea::vertical()
            .id_salt("setup_scroll")
            .show(ui, |ui| {
                if let Some(error) = self.setup.error.clone() {
                    card(ui, |ui| {
                        ui.strong("Let’s fix this before continuing");
                        ui.label(error);
                        if ui.button("View diagnostic logs").clicked() {
                            self.diagnostics_open = true;
                        }
                    });
                }
                ui.add_enabled_ui(!busy, |ui| match self.setup.progress.step {
                    0 => self.setup_welcome(ui),
                    1 => self.setup_brain(ui),
                    2 => self.setup_speech(ui),
                    3 => self.setup_audio(ui),
                    _ => self.setup_trial(ui),
                });
                if busy {
                    ui.horizontal(|ui| {
                        ui.spinner();
                        ui.label(&self.status);
                    });
                    if self.starting && ui.button("Cancel preparation").clicked() {
                        // The controller owns startup; restarting the bridge cancels its context.
                        self.backend.reset();
                        self.connected = false;
                        self.active = false;
                        self.starting = false;
                        self.setup.busy = None;
                    }
                }
                if self.setup.progress.step == 2 {
                    self.setup_download(ui);
                }
                ui.add_space(16.);
                ui.add_enabled_ui(!busy, |ui| {
                    ui.horizontal_wrapped(|ui| {
                        if self.setup.progress.step > 0
                            && !self.active
                            && ui.button("Back").clicked()
                        {
                            self.setup_save(self.setup.progress.step - 1, false, false);
                        }
                        let ready = match self.setup.progress.step {
                            0 => true,
                            1 => self.setup.provider_ready(),
                            2 => self.setup.models_ready(),
                            3 => self.active,
                            _ => false,
                        };
                        if self.setup.progress.step < 4
                            && ui
                                .add_enabled(
                                    ready,
                                    egui::Button::new(if self.setup.progress.step == 0 {
                                        "Set up together"
                                    } else {
                                        "Continue"
                                    })
                                    .fill(selected(ui)),
                                )
                                .clicked()
                        {
                            self.setup_save(self.setup.progress.step + 1, false, false);
                        }
                        if self.active && ui.button("Stop test and change setup").clicked() {
                            self.starting = true;
                            self.setup_request("stop", json!({}), Operation::Stop);
                        }
                        if !self.active && ui.button("Do this later").clicked() {
                            self.setup_save(self.setup.progress.step, false, true);
                        }
                    });
                });
            });
    }
    fn setup_welcome(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.heading("A few steps to your first conversation");
            ui.label("Choose how Obey thinks and speaks, then try a short conversation. You can use your voice or stay with text.");
            ui.add_space(12.);
            ui.strong(if self.setup.existing {
                "Your Obey Voice settings are here"
            } else {
                "Let’s make Obey yours"
            });
            ui.label(if self.setup.existing {
                "We’ll use your existing settings and personas. Changes you save here also apply to the terminal app."
            } else {
                "We’ll save your choices for this app and the Obey Voice terminal app. Nothing is downloaded until you choose to install it."
            });
            ui.label(
                RichText::new(&self.setup.config_path)
                    .small()
                    .color(muted(ui)),
            );
            if !self.setup.warning.is_empty() {
                ui.label(&self.setup.warning);
            }
            ui.add_space(12.);
            ui.label(
                "Setup resumes after you quit. You can always reopen it from Home or Settings.",
            );
            if self.setup.existing && ui.button("Keep my setup and go to Home").clicked() {
                self.setup_save(0, false, true);
            }
        });
    }
    fn setup_download(&mut self, ui: &mut egui::Ui) {
        if self.task.is_none() && self.logs.is_empty() {
            return;
        }
        let p = &self.setup.download;
        if let Some(phase) = p["phase"].as_str() {
            ui.label(format!(
                "{} · {}",
                p["asset"].as_str().unwrap_or("Speech model"),
                phase
            ));
            let bytes = p["bytes"].as_f64().unwrap_or(0.);
            let total = p["total"].as_f64().unwrap_or(0.);
            if total > 0. {
                ui.add(
                    egui::ProgressBar::new((bytes / total).clamp(0., 1.) as f32).text(format!(
                        "{:.1} / {:.1} MB",
                        bytes / 1e6,
                        total / 1e6
                    )),
                );
            } else if phase == "download" {
                ui.label("Size not reported by the download server");
            }
            if let Some(detail) = p["detail"].as_str() {
                ui.label(detail);
            }
        }
        if let Some(id) = self.task
            && ui.button("Cancel download").clicked()
        {
            self.setup.download_cancelled = true;
            self.backend.cancel(id);
            self.status = "Cancelling download…".into();
        }
        ui.collapsing("Download details", |ui| {
            egui::ScrollArea::vertical()
                .id_salt("setup_download_log")
                .max_height(220.)
                .show(ui, |ui| {
                    for line in &self.logs {
                        ui.label(RichText::new(line).monospace().small());
                    }
                });
        });
    }
}
