use super::palette;
use crate::model::Screen;
use eframe::egui;
use eframe::egui::{Color32, FontId, Pos2, Rect, Stroke, Vec2};

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
        Screen::Settings | Screen::Setup => {
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
