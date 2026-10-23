use crate::app::VoiceApp;
use crate::model::string;
use eframe::egui;

impl VoiceApp {
    pub(super) fn persona_directory_editor(&mut self, ui: &mut egui::Ui) {
        ui.label(
            egui::RichText::new(
                "Choose where new conversations work. Existing sessions keep their folder.",
            )
            .small()
            .color(crate::theme::muted(ui)),
        );
        let f = &mut self.persona_form;
        let label = if f.starting_directory.is_empty() {
            "App default".into()
        } else {
            f.directory_choices
                .iter()
                .find(|c| string(c, "path") == f.starting_directory)
                .map(|c| string(c, "name"))
                .unwrap_or_else(|| "Custom directory".into())
        };
        egui::ComboBox::from_id_salt("persona_directory")
            .selected_text(label)
            .show_ui(ui, |ui| {
                ui.selectable_value(&mut f.starting_directory, String::new(), "App default");
                for camp in &f.directory_choices {
                    ui.selectable_value(
                        &mut f.starting_directory,
                        string(camp, "path"),
                        string(camp, "name"),
                    );
                }
            });
        ui.add(
            egui::TextEdit::singleline(&mut f.starting_directory)
                .hint_text("Use app default, or enter a folder path")
                .desired_width(f32::INFINITY),
        );
        ui.horizontal(|ui| {
            if ui
                .add_enabled(
                    self.persona_form.browse_request.is_none(),
                    egui::Button::new("Browse…"),
                )
                .clicked()
            {
                self.browse_persona_directory();
            }
            if ui
                .add_enabled(
                    self.persona_form.directory_request.is_none(),
                    egui::Button::new("Refresh campaigns"),
                )
                .clicked()
            {
                self.load_persona_directories();
            }
            if self.persona_form.directory_request.is_some() {
                ui.spinner();
            }
        });
        if !self.persona_form.directory_notice.is_empty() {
            ui.label(
                egui::RichText::new(
                    "Campaigns unavailable. You can still browse or enter a folder.",
                )
                .small()
                .color(crate::theme::muted(ui)),
            )
            .on_hover_text(&self.persona_form.directory_notice);
        }
    }
}
