use super::media::{Attachment, Cached, Loaded, Media};
use eframe::egui;
use image::AnimationDecoder;
use std::io::{BufReader, Cursor, Read};
use std::time::Duration;

impl Media {
    pub fn show(&mut self, ui: &mut egui::Ui, refs: &[Attachment], draft: bool) -> Vec<String> {
        let mut removed = Vec::new();
        let response = ui.horizontal_wrapped(|ui| {
            for attachment in refs {
                if !self.cache.contains_key(&attachment.id) && self.jobs < 4 {
                    // Older turns keep an explicit reload action instead of thrashing
                    // all textures when a long transcript exceeds the preview cache.
                    if self.cache.len() >= 12
                        && ui.small_button("Load preview").clicked()
                        && let Some(id) = self
                            .cache
                            .keys()
                            .find(|id| !refs.iter().any(|r| &r.id == *id))
                            .cloned()
                    {
                        self.cache.remove(&id);
                    }
                    if self.cache.len() < 12 {
                        self.cache.insert(
                            attachment.id.clone(),
                            Cached {
                                frames: Vec::new(),
                                error: None,
                            },
                        );
                        self.jobs += 1;
                        let a = attachment.clone();
                        let tx = self.tx.clone();
                        let session = self.session.clone();
                        let ctx = ui.ctx().clone();
                        std::thread::spawn(move || {
                            let result = load(&a);
                            let _ = tx.send((session, Loaded::Preview(a.id, result)));
                            ctx.request_repaint();
                        });
                    }
                }
                ui.vertical(|ui| {
                    ui.label(&attachment.name);
                    if let Some(cached) = self.cache.get(&attachment.id) {
                        if let Some(error) = &cached.error {
                            ui.colored_label(egui::Color32::RED, error);
                        }
                        if !cached.frames.is_empty() {
                            let total: Duration =
                                cached.frames.iter().map(|(_, delay)| *delay).sum();
                            let mut elapsed = if self.paused || total.is_zero() {
                                Duration::ZERO
                            } else {
                                Duration::from_millis(
                                    (self.started.elapsed().as_millis() % total.as_millis()) as u64,
                                )
                            };
                            let mut frame = &cached.frames[0].0;
                            for (texture, delay) in &cached.frames {
                                frame = texture;
                                if elapsed < *delay {
                                    break;
                                }
                                elapsed = elapsed.saturating_sub(*delay);
                            }
                            ui.add(
                                egui::Image::new(frame)
                                    .max_size(egui::vec2(
                                        if draft { 140. } else { 320. },
                                        if draft { 75. } else { 220. },
                                    ))
                                    .maintain_aspect_ratio(true),
                            );
                            if cached.frames.len() > 1 && !self.paused {
                                ui.ctx().request_repaint_after(Duration::from_millis(40));
                            }
                        } else if cached.error.is_none() {
                            ui.spinner();
                        }
                    }
                    ui.small(format!("{}×{}", attachment.width, attachment.height));
                    if attachment.frames > 1 {
                        ui.small("GIF · model input uses first frame");
                        if ui
                            .small_button(if self.paused {
                                "Play GIFs"
                            } else {
                                "Pause GIFs"
                            })
                            .clicked()
                        {
                            self.paused = !self.paused;
                        }
                    }
                    ui.horizontal(|ui| {
                        if ui.small_button("Open original").clicked() {
                            open_original(attachment.path.clone());
                        }
                        if draft && ui.small_button("Remove").clicked() {
                            removed.push(attachment.id.clone());
                        }
                    });
                });
            }
        });
        if draft {
            let height = response.response.rect.height();
            if (self.draft_height - height).abs() > 1. {
                ui.ctx().request_repaint();
            }
            self.draft_height = height;
        }
        removed
    }
}
fn open_original(path: String) {
    std::thread::spawn(move || {
        #[cfg(target_os = "macos")]
        let _ = std::process::Command::new("open").arg(path).status();
        #[cfg(target_os = "linux")]
        let _ = std::process::Command::new("xdg-open").arg(path).status();
        #[cfg(target_os = "windows")]
        let _ = std::process::Command::new("explorer").arg(path).status();
    });
}
fn load(a: &Attachment) -> Result<Vec<(egui::ColorImage, Duration)>, String> {
    let file = std::fs::File::open(&a.path).map_err(|e| e.to_string())?;
    let mut data = Vec::new();
    file.take((8 << 20) + 1)
        .read_to_end(&mut data)
        .map_err(|e| e.to_string())?;
    if data.len() > 8 << 20 {
        return Err("Image exceeds 8 MiB".into());
    }
    if a.width == 0 || a.height == 0 || a.width > 4096 || a.height > 4096 || a.frames > 64 {
        return Err("Image dimensions exceed preview limits".into());
    }
    let frame = |rgba: image::RgbaImage| {
        let thumb = image::DynamicImage::ImageRgba8(rgba)
            .thumbnail(320, 220)
            .into_rgba8();
        egui::ColorImage::from_rgba_unmultiplied(
            [thumb.width() as usize, thumb.height() as usize],
            thumb.as_raw(),
        )
    };
    if a.frames > 1 {
        let decoder = image::codecs::gif::GifDecoder::new(BufReader::new(Cursor::new(data)))
            .map_err(|e| e.to_string())?;
        let mut out = Vec::new();
        for f in decoder.into_frames().take(65) {
            let f = f.map_err(|e| e.to_string())?;
            let (n, d) = f.delay().numer_denom_ms();
            let delay = Duration::from_millis((u64::from(n) / u64::from(d.max(1))).max(20));
            out.push((frame(f.into_buffer()), delay));
        }
        if out.len() > 64 {
            return Err("GIF exceeds 64 frames".into());
        }
        return Ok(out);
    }
    let img = image::load_from_memory(&data).map_err(|e| e.to_string())?;
    Ok(vec![(frame(img.into_rgba8()), Duration::from_secs(1))])
}
