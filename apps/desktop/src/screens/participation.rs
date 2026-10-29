use crate::app::VoiceApp;
use eframe::egui;

impl VoiceApp {
    pub(super) fn participation_controls(&mut self, ui: &mut egui::Ui) {
        let Some(settings) = self.state.participation.clone() else {
            return;
        };
        ui.horizontal_wrapped(|ui| {
            ui.label("Agent participation");
            let mut mode = settings.mode.clone();
            egui::ComboBox::from_id_salt("participation-mode")
                .selected_text(&mode)
                .show_ui(ui, |ui| {
                    for value in ["off", "invited", "open"] {
                        ui.selectable_value(&mut mode, value.to_owned(), value);
                    }
                });
            if mode != settings.mode {
                self.action("participation", &mode, false);
            }
            ui.menu_button("Participants", |ui| {
                let draft_id =
                    egui::Id::unique(("participation-member-draft", &self.state.session_id));
                if ui.button("All enabled personas").clicked() {
                    self.action("participation", &format!("{} all", settings.mode), false);
                    ui.data_mut(|data| data.remove::<Vec<String>>(draft_id));
                    ui.close();
                    return;
                }
                let personas = self.personas.clone();
                let mut selected = ui
                    .data(|data| data.get_temp::<Vec<String>>(draft_id))
                    .unwrap_or_else(|| {
                        settings.members.clone().unwrap_or_else(|| {
                            personas
                                .iter()
                                .filter(|p| !p.inactive)
                                .map(|p| p.id.clone())
                                .collect()
                        })
                    });
                for persona in personas.iter().filter(|p| !p.inactive) {
                    let mut included = selected.contains(&persona.id);
                    let can_change = if included {
                        selected.len() > 1
                    } else {
                        selected.len() < 8
                    };
                    if ui
                        .add_enabled(
                            can_change,
                            egui::Checkbox::new(&mut included, &persona.display_name),
                        )
                        .changed()
                    {
                        selected.retain(|id| id != &persona.id);
                        if included {
                            selected.push(persona.id.clone());
                        }
                    }
                }
                ui.data_mut(|data| data.insert_temp(draft_id, selected.clone()));
                if ui
                    .add_enabled(
                        !selected.is_empty() && selected.len() <= 8,
                        egui::Button::new("Apply participants"),
                    )
                    .clicked()
                {
                    self.action(
                        "participation",
                        &format!("{} {}", settings.mode, selected.join(",")),
                        false,
                    );
                    ui.data_mut(|data| data.remove::<Vec<String>>(draft_id));
                    ui.close();
                }
            });
            ui.label("Invited: join when asked · Open: one useful addition");
        });
    }
}
