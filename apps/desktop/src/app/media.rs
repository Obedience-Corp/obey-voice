//! Attachment work runs on bounded background jobs; textures stay on the UI thread.
use crate::app::VoiceApp;
use crate::model::Purpose;
use base64::Engine;
use eframe::egui;
use serde::Deserialize;
use serde_json::json;
use std::collections::HashMap;
use std::sync::mpsc;
use std::time::{Duration, Instant};

#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Attachment {
    pub id: String,
    pub name: String,
    pub path: String,
    pub width: u32,
    pub height: u32,
    pub frames: usize,
}
pub(super) enum Loaded {
    Preview(String, Result<Vec<(egui::ColorImage, Duration)>, String>),
    Clipboard(Result<String, String>),
}
pub(super) struct Cached {
    pub frames: Vec<(egui::TextureHandle, Duration)>,
    pub error: Option<String>,
}
pub struct Media {
    pub submitted: Option<String>,
    pub paused: bool,
    pub(super) cache: HashMap<String, Cached>,
    pub(super) tx: mpsc::Sender<(String, Loaded)>,
    pub(super) rx: mpsc::Receiver<(String, Loaded)>,
    pub(super) session: String,
    pub(super) started: Instant,
    pub jobs: usize,
}
impl Default for Media {
    fn default() -> Self {
        let (tx, rx) = mpsc::channel();
        Self {
            submitted: None,
            paused: false,
            cache: HashMap::new(),
            tx,
            rx,
            session: String::new(),
            started: Instant::now(),
            jobs: 0,
        }
    }
}
impl VoiceApp {
    pub fn poll_media(&mut self, ctx: &egui::Context) {
        if self.media.session != self.state.session_id {
            self.media.session = self.state.session_id.clone();
            self.media.cache.clear();
            self.media.submitted = None;
        }
        while let Ok((session, result)) = self.media.rx.try_recv() {
            self.media.jobs = self.media.jobs.saturating_sub(1);
            if session != self.state.session_id {
                continue;
            }
            match result {
                Loaded::Clipboard(Ok(data)) => {
                    self.request("attachment_add", json!({"image":data}), Purpose::Ignore);
                }
                Loaded::Clipboard(Err(error)) => self.error = Some(error),
                Loaded::Preview(id, result) => {
                    if let Some(cached) = self.media.cache.get_mut(&id) {
                        match result {
                            Ok(frames) => {
                                cached.frames = frames
                                    .into_iter()
                                    .enumerate()
                                    .map(|(i, (frame, delay))| {
                                        (
                                            ctx.load_texture(
                                                format!("{id}-{i}"),
                                                frame,
                                                egui::TextureOptions::LINEAR,
                                            ),
                                            delay,
                                        )
                                    })
                                    .collect()
                            }
                            Err(error) => cached.error = Some(error),
                        }
                    }
                }
            }
        }
        if !self.state.attachment_busy
            && self.state.attachments.is_empty()
            && let Some(sent) = self.media.submitted.take()
            && self.composer.trim() == sent
        {
            self.composer.clear();
        }
        if !self.state.attachment_error.is_empty() && !self.state.attachment_busy {
            self.media.submitted = None;
        }
    }
    pub fn media_paste(&mut self, ui: &mut egui::Ui) {
        let (images, paths) = take_paste(ui);
        for text in paths {
            self.action("attachment_paste", &text, false);
        }
        for img in images {
            if self.media.jobs >= 4 || img.size[0].saturating_mul(img.size[1]) > 16 << 20 {
                self.error =
                    Some("Wait for image loading, or use an image below 16 megapixels".into());
                continue;
            }
            self.media.jobs += 1;
            let tx = self.media.tx.clone();
            let session = self.state.session_id.clone();
            let ctx = ui.ctx().clone();
            std::thread::spawn(move || {
                let result = (|| {
                    let data: Vec<u8> = img
                        .pixels
                        .iter()
                        .flat_map(|p| p.to_srgba_unmultiplied())
                        .collect();
                    let rgba =
                        image::RgbaImage::from_raw(img.size[0] as u32, img.size[1] as u32, data)
                            .ok_or("Invalid clipboard image")?;
                    let mut out = std::io::Cursor::new(Vec::new());
                    image::DynamicImage::ImageRgba8(rgba)
                        .write_to(&mut out, image::ImageFormat::Png)
                        .map_err(|e| e.to_string())?;
                    if out.get_ref().len() > 8 << 20 {
                        return Err("Clipboard image exceeds 8 MiB".into());
                    }
                    Ok(base64::engine::general_purpose::STANDARD.encode(out.into_inner()))
                })();
                let _ = tx.send((session, Loaded::Clipboard(result)));
                ctx.request_repaint();
            });
        }
        let dropped = ui.input(|i| i.raw.dropped_files.clone());
        let paths: Vec<_> = dropped
            .iter()
            .map(|f| f.path())
            .map(|p| p.to_string_lossy().into_owned())
            .collect();
        if !paths.is_empty() {
            self.request("attachment_add", json!({"paths":paths}), Purpose::Ignore);
        }
    }
}
fn take_paste(ui: &mut egui::Ui) -> (Vec<std::sync::Arc<egui::ColorImage>>, Vec<String>) {
    let mut images = Vec::new();
    let mut paths = Vec::new();
    if ui.memory(|m| m.has_focus(ui.make_persistent_id("composer"))) {
        ui.input_mut(|i| {
            i.events.retain(|event| match event {
                egui::Event::PasteImage(img) => {
                    images.push(img.clone());
                    false
                }
                egui::Event::Paste(text) if image_path_paste(text) => {
                    paths.push(text.clone());
                    false
                }
                _ => true,
            })
        });
    }
    (images, paths)
}
pub(super) fn image_path_paste(text: &str) -> bool {
    let text = text.trim();
    let path = text.trim_end_matches(['\'', '"']).to_lowercase();
    (text.starts_with('/')
        || text.starts_with("~/")
        || text.starts_with("file://")
        || text.starts_with(['\'', '"']))
        && [".png", ".jpg", ".jpeg", ".gif"]
            .iter()
            .any(|ext| path.ends_with(ext))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn path_paste_preserves_normal_prose() {
        assert!(image_path_paste("/tmp/Screenshot\\ with\\ spaces.png"));
        assert!(image_path_paste("\"/tmp/animation.gif\""));
        assert!(!image_path_paste("Please inspect image.png"));
        assert!(!image_path_paste("/help"));
    }
    #[test]
    fn native_paste_routes_images_only_when_composer_has_focus() {
        let ctx = egui::Context::default();
        let image = std::sync::Arc::new(egui::ColorImage::filled([2, 2], egui::Color32::RED));
        for focused in [false, true] {
            let input = egui::RawInput {
                events: vec![
                    egui::Event::PasteImage(image.clone()),
                    egui::Event::Paste("/tmp/image with spaces.png".into()),
                    egui::Event::Paste("ordinary text".into()),
                ],
                ..Default::default()
            };
            let mut output = ctx.run_ui(input, |ui| {
                egui::CentralPanel::default().show(ui, |ui| {
                    let id = ui.make_persistent_id("composer");
                    if focused {
                        ui.memory_mut(|m| m.request_focus(id));
                    } else {
                        ui.memory_mut(|m| m.surrender_focus(id));
                    }
                    let (images, paths) = take_paste(ui);
                    assert_eq!(images.len(), usize::from(focused));
                    assert_eq!(paths.len(), usize::from(focused));
                    let remaining = ui.input(|i| i.events.clone());
                    assert!(
                        remaining
                            .iter()
                            .any(|e| matches!(e, egui::Event::Paste(s) if s == "ordinary text"))
                    );
                    if !focused {
                        assert_eq!(remaining.len(), 3);
                    }
                });
            });
            output.textures_delta.clear();
        }
    }
}
