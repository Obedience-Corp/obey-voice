use super::*;
use eframe::egui::{Pos2, Rect};

#[test]
fn theme_switch_reuses_warm_glyph_atlas() {
    let ctx = egui::Context::default();
    apply(&ctx, false);
    let mut warm = ctx.run_ui(egui::RawInput::default(), |ui| {
        ui.label("Reusable text glyphs");
    });
    warm.textures_delta.clear();
    for dark in [true, false, true, false] {
        apply(&ctx, dark);
        let mut output = ctx.run_ui(egui::RawInput::default(), |ui| {
            ui.label("Reusable text glyphs");
        });
        let changed = !output.textures_delta.set.is_empty();
        output.textures_delta.clear();
        assert!(
            !changed,
            "switching themes must not rebuild/upload the font atlas"
        );
    }
}

#[test]
fn theme_switch_discards_old_panel_colors_in_both_directions() {
    let ctx = egui::Context::default();
    apply(&ctx, false);
    let text_options = ctx.global_style().visuals.text_options;
    let rect = Rect::from_min_size(Pos2::ZERO, egui::vec2(100., 100.));
    // Repeated switches exercise both newly initialized and cached themes.
    for dark in [true, false, true, false] {
        let mut pending = true;
        let mut output = ctx.run_ui(egui::RawInput::default(), |ui| {
            // Like the sidebar, this is drawn before the theme toggle.
            ui.painter().rect_filled(rect, 0, card(ui));
            if pending {
                pending = false;
                switch(ui.ctx(), dark);
                return;
            }
            assert_eq!(ui.visuals().text_options, text_options);
            assert_eq!(ui.visuals().dark_mode, dark);
            assert_eq!(ui.visuals().override_text_color, Some(palette(dark).ink));
            ui.painter().rect_filled(rect, 0, palette(dark).surface);
        });
        output.textures_delta.clear();
        assert_eq!(output.platform_output.num_completed_passes, 2);
        let fills: Vec<_> = output
            .shapes
            .iter()
            .filter_map(|shape| {
                if let egui::Shape::Rect(painted) = &shape.shape {
                    (painted.rect == rect).then_some(painted.fill)
                } else {
                    None
                }
            })
            .collect();
        assert_eq!(fills, vec![palette(dark).card, palette(dark).surface]);
    }
}
