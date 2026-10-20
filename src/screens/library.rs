use super::{card, combo, field};
use crate::app::VoiceApp;
use crate::model::{Purpose, Screen, display, string};
use crate::theme::{muted, selected};
use eframe::egui;
use eframe::egui::RichText;

impl VoiceApp {
    pub(super) fn library_screen(&mut self, ui: &mut egui::Ui) {
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
    pub(super) fn audiobook(&mut self, ui: &mut egui::Ui) {
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
}
