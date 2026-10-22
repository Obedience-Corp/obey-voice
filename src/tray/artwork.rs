//! Corp mark and small state badges; no renderer dependency at runtime.
use super::status::Activity;

pub fn rgba(activity: Activity, template: bool) -> Vec<u8> {
    let mut icon = eframe::icon_data::from_png_bytes(if template {
        include_bytes!("../../assets/tray-template.png")
    } else {
        include_bytes!("../../assets/tray-color.png")
    })
    .expect("bundled tray artwork must be a valid PNG");
    let color = match activity {
        Activity::Idle => return icon.rgba,
        Activity::Listening => [0, 170, 130, 255],
        Activity::Recording => [235, 55, 68, 255],
        Activity::Paused => [240, 174, 30, 255],
        Activity::Error => [240, 110, 30, 255],
    };
    for y in 22..32_i32 {
        for x in 22..32_i32 {
            let distance = (x - 26).pow(2) + (y - 26).pow(2);
            let i = ((y * 32 + x) * 4) as usize;
            if distance <= 25 {
                icon.rgba[i..i + 4].copy_from_slice(&[0, 0, 0, 0]);
            }
            if distance <= 16 {
                icon.rgba[i..i + 4].copy_from_slice(&color);
            }
        }
    }
    icon.rgba
}
