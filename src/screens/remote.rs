use super::card;
use crate::app::VoiceApp;
use crate::model::Purpose;
use crate::theme::{accent, muted};
use eframe::egui;
use eframe::egui::RichText;
use serde_json::Value;

impl VoiceApp {
    pub(super) fn remote_screen(&mut self, ui: &mut egui::Ui) {
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
}
