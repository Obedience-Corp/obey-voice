//! Screen routing and shared controls. Each workflow owns its rendering module.
mod chat;
mod diagnostics;
mod dialogs;
mod home;
mod library;
mod meeting;
mod persona_audio;
mod persona_directory;
mod persona_editor;
mod personas;
mod provider_choices;
mod remote;
mod settings;
mod setup;
mod work_cue;
mod workers;

use crate::app::VoiceApp;
use crate::model::Screen;
use crate::theme::muted;
use eframe::egui;
use eframe::egui::RichText;

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
            Screen::Setup => self.setup_screen(ui),
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
}

mod participation;
