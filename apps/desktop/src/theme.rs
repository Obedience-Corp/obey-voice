//! Palette, font setup, and egui styling shared by all screens.
#[cfg(target_os = "macos")]
mod native;
mod painting;
#[cfg(test)]
mod tests;

use eframe::egui;
use eframe::egui::Color32;
use eframe::egui::FontId;
use eframe::egui::Stroke;
#[cfg(target_os = "macos")]
pub use native::native_appearance;
#[cfg(target_os = "macos")]
use native::native_backdrop;
pub use painting::{backdrop, nav, orb, waveform};

#[derive(Clone, Copy)]
pub struct Palette {
    pub ink: Color32,
    pub accent: Color32,
    pub muted: Color32,
    pub card: Color32,
    pub border: Color32,
    pub selected: Color32,
    pub surface: Color32,
}
pub fn palette(dark: bool) -> Palette {
    if dark {
        Palette {
            ink: Color32::from_rgb(232, 235, 246),
            accent: Color32::from_rgb(163, 182, 255),
            muted: Color32::from_rgb(160, 170, 194),
            card: Color32::from_rgba_unmultiplied(62, 69, 91, 110),
            border: Color32::from_white_alpha(28),
            selected: Color32::from_rgba_unmultiplied(87, 108, 170, 120),
            surface: Color32::from_rgba_unmultiplied(28, 32, 44, 115),
        }
    } else {
        Palette {
            ink: Color32::from_rgb(39, 46, 65),
            accent: Color32::from_rgb(63, 93, 210),
            muted: Color32::from_rgb(105, 113, 136),
            card: Color32::from_white_alpha(130),
            border: Color32::from_white_alpha(220),
            selected: Color32::from_rgb(220, 229, 253),
            surface: Color32::from_white_alpha(95),
        }
    }
}
pub fn accent(ui: &egui::Ui) -> Color32 {
    palette(ui.visuals().dark_mode).accent
}
pub fn muted(ui: &egui::Ui) -> Color32 {
    palette(ui.visuals().dark_mode).muted
}
pub fn card(ui: &egui::Ui) -> Color32 {
    palette(ui.visuals().dark_mode).card
}
pub fn selected(ui: &egui::Ui) -> Color32 {
    palette(ui.visuals().dark_mode).selected
}

pub fn setup(cc: &eframe::CreationContext<'_>, dark: bool) {
    let ctx = &cc.egui_ctx;
    // We update AppKit explicitly, without a second appearance change from winit.
    #[cfg(target_os = "macos")]
    ctx.options_mut(|o| o.sync_window_theme = false);

    // Use the locally installed system font, without redistributing Apple fonts.
    #[cfg(target_os = "macos")]
    if let Ok(bytes) = std::fs::read("/System/Library/Fonts/SFNS.ttf") {
        let mut fonts = egui::FontDefinitions::default();
        fonts
            .font_data
            .insert("system".into(), egui::FontData::from_owned(bytes).into());
        fonts
            .families
            .entry(egui::FontFamily::Proportional)
            .or_default()
            .insert(0, "system".into());
        ctx.set_fonts(fonts);
    }
    apply(ctx, dark);
    #[cfg(target_os = "macos")]
    {
        native_backdrop(cc);
        native_appearance(cc, dark);
    }
}

pub fn apply(ctx: &egui::Context, dark: bool) {
    let theme = if dark {
        egui::Theme::Dark
    } else {
        egui::Theme::Light
    };
    ctx.set_theme(theme);
    ctx.set_visuals(if dark {
        egui::Visuals::dark()
    } else {
        egui::Visuals::light()
    });
    let c = palette(dark);
    ctx.style_mut_of(theme, |s| {
        let v = &mut s.visuals;
        // egui's theme defaults use different coverage transfer functions.
        // Changing TextOptions clears the glyph atlas and rerasterizes SFNS.
        // Keep text rendering stable; only foreground/background colors change.
        v.text_options = egui::Visuals::dark().text_options;
        v.override_text_color = Some(c.ink);
        v.panel_fill = Color32::TRANSPARENT;
        v.window_fill = if dark {
            Color32::from_rgb(36, 40, 54)
        } else {
            Color32::from_rgb(244, 246, 252)
        };
        v.window_corner_radius = 22.into();
        v.window_stroke = Stroke::new(1., c.border);
        v.extreme_bg_color = if dark {
            Color32::from_black_alpha(60)
        } else {
            Color32::from_white_alpha(150)
        };
        v.faint_bg_color = c.surface;
        v.selection.bg_fill = c.selected;
        v.selection.stroke = Stroke::new(1., c.accent);
        for w in [
            &mut v.widgets.inactive,
            &mut v.widgets.hovered,
            &mut v.widgets.active,
            &mut v.widgets.open,
        ] {
            w.corner_radius = 12.into();
            w.bg_stroke = Stroke::new(1., c.border);
            w.fg_stroke = Stroke::new(1.3, c.ink);
        }
        v.widgets.inactive.bg_fill = c.card;
        v.widgets.inactive.weak_bg_fill = c.card;
        v.widgets.hovered.bg_fill = c.selected;
        v.widgets.hovered.weak_bg_fill = c.selected;
        v.widgets.active.bg_fill = c.selected;
        v.widgets.active.weak_bg_fill = v.widgets.active.bg_fill;
        v.widgets.noninteractive.bg_stroke =
            Stroke::new(0.8, Color32::from_rgba_unmultiplied(115, 126, 162, 27));
        v.widgets.noninteractive.fg_stroke = Stroke::new(1., c.muted);
        v.widgets.noninteractive.corner_radius = 12.into();
        v.window_shadow = egui::epaint::Shadow {
            offset: [0, 12],
            blur: 40,
            spread: 0,
            color: Color32::from_black_alpha(35),
        };
        s.spacing.item_spacing = egui::vec2(10., 12.);
        s.spacing.button_padding = egui::vec2(16., 10.);
        s.spacing.interact_size.y = 34.;
        s.text_styles
            .insert(egui::TextStyle::Body, FontId::proportional(15.));
        s.text_styles
            .insert(egui::TextStyle::Button, FontId::proportional(14.));
        s.text_styles
            .insert(egui::TextStyle::Small, FontId::proportional(12.));
        s.text_styles
            .insert(egui::TextStyle::Heading, FontId::proportional(28.));
    });
}

/// Rebuild the root Ui before presenting a theme change. Existing Uis keep
/// their previous Arc<Style> even after the context's style changes.
pub fn switch(ctx: &egui::Context, dark: bool) {
    apply(ctx, dark);
    ctx.request_discard("appearance changed; rebuild all panels with the new style");
    ctx.request_repaint();
}

pub fn dialog(dark: bool) -> egui::Frame {
    glass(dark).inner_margin(28).fill(if dark {
        Color32::from_rgba_unmultiplied(30, 35, 49, 248)
    } else {
        Color32::from_rgba_unmultiplied(247, 249, 255, 248)
    })
}

pub fn glass(dark: bool) -> egui::Frame {
    let c = palette(dark);
    egui::Frame::new()
        .fill(c.card)
        .inner_margin(22)
        .corner_radius(22)
        .stroke(Stroke::new(1., c.border))
        .shadow(egui::epaint::Shadow {
            offset: [0, 5],
            blur: 18,
            spread: 0,
            color: Color32::from_rgba_unmultiplied(70, 85, 130, 10),
        })
}

// Soft color is painted beneath the translucent surfaces. The actual desktop
// blur is supplied by NSVisualEffectView, not by a screenshot or a fake texture.
