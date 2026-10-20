use crate::model::Screen;
use eframe::egui::{self, Color32, FontId, Pos2, Rect, Stroke, Vec2};

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
pub fn backdrop(ui: &egui::Ui) {
    let r = ui.max_rect();
    let p = ui.painter();
    let dark = ui.visuals().dark_mode;
    p.rect_filled(
        r,
        14,
        if dark {
            Color32::from_rgba_unmultiplied(18, 22, 33, 220)
        } else {
            Color32::from_rgba_unmultiplied(238, 241, 251, 195)
        },
    );
    for (center, color, radius) in [
        (
            r.left_top() + egui::vec2(r.width() * 0.45, 70.),
            [151, 177, 248],
            400.,
        ),
        (
            r.right_bottom() - egui::vec2(80., 40.),
            [184, 170, 239],
            370.,
        ),
        (
            r.left_bottom() + egui::vec2(170., -120.),
            [158, 216, 214],
            270.,
        ),
    ] {
        for i in (1..=24).rev() {
            p.circle_filled(
                center,
                radius * i as f32 / 24.,
                Color32::from_rgba_unmultiplied(
                    color[0],
                    color[1],
                    color[2],
                    if dark { 1 } else { 2 },
                ),
            );
        }
    }
}

pub fn waveform(p: &egui::Painter, center: Pos2, scale: f32, color: Color32) {
    for (i, height) in [10., 24., 38., 20., 30., 14.].iter().enumerate() {
        let x = center.x + (i as f32 - 2.5) * 8. * scale;
        p.rect_filled(
            Rect::from_center_size(
                egui::pos2(x, center.y),
                egui::vec2(4. * scale, height * scale),
            ),
            8,
            color,
        );
    }
}

pub fn orb(p: &egui::Painter, center: Pos2, radius: f32, dark: bool) {
    let c = palette(dark);
    for i in (1..=20).rev() {
        p.circle_filled(
            center + egui::vec2(0., 10.),
            radius + i as f32 * 2.,
            Color32::from_rgba_unmultiplied(115, 135, 228, 3),
        );
    }
    p.circle_filled(
        center,
        radius,
        if dark {
            Color32::from_rgba_unmultiplied(83, 99, 163, 140)
        } else {
            Color32::from_rgba_unmultiplied(211, 218, 251, 150)
        },
    );
    for i in (1..=35).rev() {
        let f = i as f32 / 35.;
        p.circle_filled(
            center + egui::vec2(-radius * 0.15, -radius * 0.17),
            radius * f * 0.8,
            Color32::from_white_alpha(4),
        );
    }
    p.circle_stroke(
        center,
        radius,
        Stroke::new(1.2, Color32::from_white_alpha(230)),
    );
    waveform(p, center, radius / 60., c.accent);
}

pub fn nav(ui: &mut egui::Ui, screen: Screen, selected: bool) -> egui::Response {
    let (r, response) =
        ui.allocate_exact_size(egui::vec2(ui.available_width(), 42.), egui::Sense::click());
    response.widget_info(|| {
        egui::WidgetInfo::selected(egui::Role::Tab, ui.is_enabled(), selected, screen.title())
    });
    let p = ui.painter();
    let c = palette(ui.visuals().dark_mode);
    if selected || response.hovered() {
        p.rect_filled(r, 13, if selected { c.card } else { c.surface });
        if selected {
            p.rect_stroke(r, 13, Stroke::new(1., c.border), egui::StrokeKind::Inside);
        }
    }
    let color = if selected { c.accent } else { c.muted };
    icon(p, r.left_center() + egui::vec2(22., 0.), screen, color);
    p.text(
        r.left_center() + egui::vec2(45., 0.),
        egui::Align2::LEFT_CENTER,
        screen.title(),
        FontId::proportional(14.),
        if selected { c.ink } else { c.muted },
    );
    response
}

fn icon(p: &egui::Painter, c: Pos2, screen: Screen, color: Color32) {
    let s = Stroke::new(1.5, color);
    let line = |a: Vec2, b: Vec2| {
        p.line_segment([c + a, c + b], s);
    };
    match screen {
        Screen::Home => {
            line(egui::vec2(-8., -1.), egui::vec2(0., -8.));
            line(egui::vec2(0., -8.), egui::vec2(8., -1.));
            p.rect_stroke(
                Rect::from_min_max(c + egui::vec2(-6., -1.), c + egui::vec2(6., 7.)),
                2,
                s,
                egui::StrokeKind::Inside,
            );
        }
        Screen::Chat | Screen::Meeting => waveform(p, c, 0.35, color),
        Screen::Personas => {
            p.circle_stroke(c + egui::vec2(0., -4.), 3.5, s);
            p.rect_stroke(
                Rect::from_center_size(c + egui::vec2(0., 5.), egui::vec2(14., 7.)),
                5,
                s,
                egui::StrokeKind::Inside,
            );
        }
        Screen::Settings => {
            p.circle_stroke(c, 7., s);
            p.circle_stroke(c, 2.5, s);
            for i in 0..8 {
                let d = Vec2::angled(i as f32 * std::f32::consts::TAU / 8.);
                line(d * 7., d * 10.);
            }
        }
        Screen::Remote => {
            p.rect_stroke(
                Rect::from_center_size(c, egui::vec2(11., 18.)),
                3,
                s,
                egui::StrokeKind::Inside,
            );
            line(egui::vec2(-2., 5.), egui::vec2(2., 5.));
        }
        Screen::Workers => {
            for x in [-5., 5.] {
                for y in [-5., 5.] {
                    p.rect_stroke(
                        Rect::from_center_size(c + egui::vec2(x, y), egui::vec2(6., 6.)),
                        2,
                        s,
                        egui::StrokeKind::Inside,
                    );
                }
            }
        }
        _ => {
            p.rect_stroke(
                Rect::from_center_size(c, egui::vec2(14., 17.)),
                3,
                s,
                egui::StrokeKind::Inside,
            );
            for y in [-3., 1., 5.] {
                line(egui::vec2(-3., y), egui::vec2(3., y));
            }
        }
    }
}

#[cfg(target_os = "macos")]
fn native_backdrop(cc: &eframe::CreationContext<'_>) {
    use objc2::MainThreadMarker;
    use objc2_app_kit::{
        NSAutoresizingMaskOptions, NSView, NSVisualEffectBlendingMode, NSVisualEffectMaterial,
        NSVisualEffectState, NSVisualEffectView, NSWindowOrderingMode,
    };
    use raw_window_handle::{HasWindowHandle, RawWindowHandle};
    let (Some(mtm), Ok(handle)) = (MainThreadMarker::new(), cc.window_handle()) else {
        return;
    };
    let RawWindowHandle::AppKit(handle) = handle.as_raw() else {
        return;
    };
    // SAFETY: eframe owns this NSView for the lifetime of CreationContext and
    // calls this on AppKit's main thread. The parent retains the inserted view.
    // Insert below the render view, keeping the backdrop out of the input hierarchy.
    unsafe {
        let view: &NSView = handle.ns_view.cast().as_ref();
        let Some(parent) = view.superview() else {
            return;
        };
        let effect = NSVisualEffectView::initWithFrame(mtm.alloc(), view.frame());
        effect.setMaterial(NSVisualEffectMaterial::Sidebar);
        effect.setBlendingMode(NSVisualEffectBlendingMode::BehindWindow);
        effect.setState(NSVisualEffectState::FollowsWindowActiveState);
        effect.setAutoresizingMask(
            NSAutoresizingMaskOptions::ViewWidthSizable
                | NSAutoresizingMaskOptions::ViewHeightSizable,
        );
        parent.addSubview_positioned_relativeTo(&effect, NSWindowOrderingMode::Below, Some(view));
    }
}

#[cfg(target_os = "macos")]
pub fn native_appearance(window: &impl raw_window_handle::HasWindowHandle, dark: bool) {
    use objc2_app_kit::{
        NSAppearance, NSAppearanceCustomization, NSAppearanceNameAqua, NSAppearanceNameDarkAqua,
        NSView,
    };
    use raw_window_handle::RawWindowHandle;
    let Ok(handle) = window.window_handle() else {
        return;
    };
    let RawWindowHandle::AppKit(handle) = handle.as_raw() else {
        return;
    };
    // SAFETY: called on eframe's main thread while the window is alive.
    unsafe {
        let view: &NSView = handle.ns_view.cast().as_ref();
        if let Some(window) = view.window() {
            let appearance = NSAppearance::appearanceNamed(if dark {
                NSAppearanceNameDarkAqua
            } else {
                NSAppearanceNameAqua
            });
            window.setAppearance(appearance.as_deref());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn theme_switch_discards_old_panel_colors_in_both_directions() {
        let ctx = egui::Context::default();
        apply(&ctx, false);
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
}
