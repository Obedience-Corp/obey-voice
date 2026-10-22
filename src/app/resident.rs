//! App actions and window lifecycle shared by native tray adapters.
use super::VoiceApp;
use crate::model::{Purpose, Screen};
use crate::tray::{Action, Snapshot};
use eframe::egui::{self, ViewportCommand};
use serde_json::json;

impl VoiceApp {
    pub(super) fn tray_snapshot(&self) -> Snapshot {
        Snapshot {
            connected: self.connected,
            setup: self.screen == Screen::Setup,
            transitioning: self.starting
                || self
                    .pending
                    .values()
                    .any(|p| matches!(p, Purpose::MeetingPaused(_)))
                || self.resident.quitting,
            conversation: self.active,
            microphone: self.state.mic,
            voice_available: self.state.voice_available && !self.text_only,
            meeting: self.meeting_active,
            paused: self.meeting_paused,
            error: self.error.is_some() || (self.active && self.state.error),
        }
    }
    pub(super) fn resident_logic(&mut self, ctx: &egui::Context) {
        self.resident.sync(&self.tray_snapshot());
        // A disabled preference, failed service, or vanished Linux panel must
        // never leave the only window unreachable.
        if self.resident.hidden && !self.resident.available() {
            self.show_window(ctx);
        }
        while let Some(action) = self.resident.receive() {
            if self.tray_snapshot().enabled(action) {
                self.tray_action(ctx, action);
            }
        }
        if ctx.input_mut(|i| i.consume_key(egui::Modifiers::COMMAND, egui::Key::Q)) {
            self.quit(ctx);
        }
        if ctx.input(|i| i.viewport().close_requested())
            && crate::tray::hide_on_close(
                self.resident.enabled,
                self.resident.available(),
                self.resident.quitting,
            )
        {
            ctx.send_viewport_cmd(ViewportCommand::CancelClose);
            ctx.send_viewport_cmd(ViewportCommand::Visible(false));
            if !self.resident.hidden {
                crate::diagnostics::record(
                    "info",
                    "window.hide",
                    json!({"activity":self.tray_snapshot().title()}),
                );
            }
            self.resident.hidden = true;
        }
        self.resident.sync(&self.tray_snapshot());
    }
    fn show_window(&mut self, ctx: &egui::Context) {
        self.resident.hidden = false;
        ctx.send_viewport_cmd(ViewportCommand::Visible(true));
        ctx.send_viewport_cmd(ViewportCommand::Minimized(false));
        ctx.send_viewport_cmd(ViewportCommand::Focus);
        ctx.request_repaint();
    }
    fn tray_action(&mut self, ctx: &egui::Context, action: Action) {
        crate::diagnostics::record(
            "info",
            "tray.action",
            json!({"action":format!("{action:?}")}),
        );
        match action {
            Action::Open => self.show_window(ctx),
            Action::Conversation => {
                self.show_window(ctx);
                self.navigate(Screen::Home);
                self.new_session = Some(String::new());
                self.refresh();
            }
            Action::Meeting => {
                self.show_window(ctx);
                if self.meeting_active {
                    self.stop_meeting();
                }
                self.navigate(Screen::Meeting);
            }
            Action::Microphone if self.meeting_active => self.pause_meeting(),
            Action::Microphone => self.action("mic", "", !self.state.mic),
            Action::Settings => {
                self.show_window(ctx);
                self.navigate(Screen::Settings);
            }
            Action::Diagnostics => {
                self.show_window(ctx);
                self.diagnostics_open = true;
            }
            Action::Quit => self.quit(ctx),
        }
    }
    pub(super) fn quit(&mut self, ctx: &egui::Context) {
        self.resident.quitting = true;
        // Backend::drop sends shutdown and waits for engine-owned session and
        // meeting persistence, then reaps all owned processes.
        crate::diagnostics::record("info", "desktop.quit", json!({}));
        ctx.send_viewport_cmd(ViewportCommand::Close);
    }
    pub fn stop_meeting(&mut self) {
        self.starting = !self.demo;
        self.request("meeting_stop", json!({}), Purpose::MeetingStop);
        if self.demo {
            self.meeting_active = false;
        }
    }
    pub fn pause_meeting(&mut self) {
        self.request(
            if self.meeting_paused {
                "meeting_resume"
            } else {
                "meeting_pause"
            },
            json!({}),
            Purpose::MeetingPaused(!self.meeting_paused),
        );
        if self.demo {
            self.meeting_paused = !self.meeting_paused;
        }
    }
    pub fn desktop_settings(&mut self, ui: &mut egui::Ui) {
        ui.strong("Desktop presence");
        if ui
            .checkbox(&mut self.resident.enabled, "Keep in menu bar / tray")
            .changed()
        {
            self.show_window(ui.ctx());
            self.resident.sync(&self.tray_snapshot());
        }
        ui.label(if self.resident.available() {
            "Closing the window keeps Obey running and any active microphone or meeting recording on. Choose Quit Obey to stop and exit."
        } else {
            "Closing the window quits Obey. A running menu bar / tray icon is required to keep it in the background."
        });
        if self.resident.enabled && !self.resident.available() {
            ui.label("No tray is available. Closing the window will quit Obey.");
        }
        if let Some(error) = &self.resident.failure {
            ui.label(error);
        }
        if ui.button("Quit Obey").clicked() {
            self.quit(ui.ctx());
        }
    }
}
