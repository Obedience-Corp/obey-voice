use crate::app::VoiceApp;
use crate::screens::card;
use crate::setup::Operation;
use eframe::egui;
use serde_json::json;

impl VoiceApp {
    pub(super) fn setup_audio(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.heading(
                if self.setup.progress.text_only && self.setup.progress.silent {
                    "Ready for a text conversation"
                } else {
                    "Let’s check your audio"
                },
            );
            if !self.active {
                ui.label("Start a test using your saved provider and persona. You’ll send a greeting on the next step.");
                if !self.setup.progress.text_only {
                    ui.label("Starting the test opens your microphone. Allow microphone access if macOS asks, then speak to check the level. Anything you say can become part of this test conversation.");
                }
                if ui.button("Start test conversation").clicked() {
                    self.setup_start();
                }
            } else {
                self.setup_audio_status(ui);
            }
            self.setup_permission_help(ui);
        });
        if !(self.active || self.setup.progress.text_only && self.setup.progress.silent) {
            self.setup_fields(ui, &["input_device", "output_device", "playback_volume"]);
        }
    }
    pub(super) fn setup_trial(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.heading("Say hello to Obey");
            ui.label("Send a short greeting to check that your provider can reply. This uses your selected account and saves a normal conversation in Sessions.");
            if !self.active {
                ui.label(
                    "The test stopped when the app closed. Start it again to verify your setup.",
                );
                if ui.button("Start test conversation").clicked() {
                    self.setup_start();
                }
                return;
            }
            if ui
                .add_enabled(!self.state.busy, egui::Button::new("Send test greeting"))
                .clicked()
            {
                self.setup_send_test();
            }
            if self.state.busy {
                ui.spinner();
            }
            if self.state.error {
                ui.strong("The test needs attention");
                ui.label(&self.state.status);
                ui.label("Check provider login, model availability, and audio permissions. Retry the greeting, or stop the test to change your setup.");
            }
            if let Some(reply) = self
                .state
                .turns
                .iter()
                .rev()
                .find(|t| t.role == "assistant")
            {
                ui.label(&reply.content);
            }
            self.setup_audio_status(ui);
            self.setup_permission_help(ui);
            if self.setup.reply_received {
                ui.strong("Your provider replied successfully");
            }
            if ui
                .add_enabled(
                    self.setup.can_finish() && !self.state.busy && !self.state.error,
                    egui::Button::new("Finish setup"),
                )
                .clicked()
            {
                self.starting = true;
                self.setup_request("stop", json!({}), Operation::Finish);
            }
        });
    }
    fn setup_audio_status(&mut self, ui: &mut egui::Ui) {
        if !self.setup.progress.text_only {
            ui.add(
                egui::ProgressBar::new(self.state.input_level.clamp(0., 1.))
                    .text("Microphone level"),
            );
            if self.setup.progress.step == 3
                && ui
                    .button(if self.state.mic {
                        "Mute microphone"
                    } else {
                        "Enable microphone"
                    })
                    .clicked()
            {
                self.action("mic", "", !self.state.mic);
            }
            ui.checkbox(
                &mut self.setup.mic_confirmed,
                "I can see the microphone level respond to my voice",
            );
        }
        if !self.setup.progress.silent {
            ui.label("The test greeting on the final step is your playback sample.");
            if self.state.muted && ui.button("Enable spoken replies").clicked() {
                self.action("audio", "", true);
            }
            ui.add_enabled(
                self.setup.reply_received,
                egui::Checkbox::new(
                    &mut self.setup.speaker_confirmed,
                    "I heard the spoken reply",
                ),
            );
        }
        ui.label(&self.state.status);
    }
    fn setup_permission_help(&self, ui: &mut egui::Ui) {
        if self.setup.progress.text_only {
            return;
        }
        ui.collapsing("No microphone signal?", |ui| {
            ui.label("Allow Obey Voice Agent in System Settings → Privacy & Security → Microphone. Check your input device and unmute the microphone. Stop and restart the test after changing permissions.");
            #[cfg(target_os = "macos")]
            ui.hyperlink_to("Open microphone permissions", "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone");
        });
    }
}
