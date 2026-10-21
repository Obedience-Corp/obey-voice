use crate::app::VoiceApp;
use crate::model::Screen;
use crate::theme::{accent, muted};
use eframe::egui;
use eframe::egui::{Color32, RichText};
use serde_json::json;
use std::time::Duration;

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
        self.setup_observe();
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
        let mut requested_dark = self.dark;
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
                    let enabled = !self.starting
                        && self.screen != Screen::Setup
                        && (!self.meeting_active || screen == Screen::Meeting);
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
                let enabled =
                    !self.starting && !self.meeting_active && self.screen != Screen::Setup;
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
                    ui.selectable_value(&mut requested_dark, false, "Light");
                    ui.selectable_value(&mut requested_dark, true, "Dark");
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
                            "Engine connected"
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
        if requested_dark != self.dark {
            self.dark = requested_dark;
            crate::theme::switch(&ctx, self.dark);
            #[cfg(target_os = "macos")]
            crate::theme::native_appearance(_frame, self.dark);
            // eframe saves this preference through App::save; never wait for disk here.
            crate::diagnostics::record("info", "appearance", json!({"dark": self.dark}));
            return;
        }
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
                                ui.horizontal(|ui| {
                                    if ui.small_button("Diagnostics").clicked() {
                                        self.diagnostics_open = true;
                                    }
                                    ui.label(
                                        RichText::new(&self.status).size(11.).color(muted(ui)),
                                    );
                                });
                            });
                        self.render_screen(ui);
                    });
            });
        self.dialogs(&ctx);
        self.diagnostics_dialog(&ctx);
        // Keep setup status and native accessibility input responsive between async checks.
        if self.starting || self.active || self.meeting_active || self.screen == Screen::Setup {
            ctx.request_repaint_after(Duration::from_millis(100));
        }
    }
}
