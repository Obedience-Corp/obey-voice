mod app;
mod backend;
mod diagnostics;
mod model;
mod screens;
mod theme;

fn main() -> eframe::Result {
    let _diagnostics = diagnostics::init();
    let options = eframe::NativeOptions {
        viewport: eframe::egui::ViewportBuilder::default()
            .with_inner_size([1180.0, 800.0])
            .with_min_inner_size([820.0, 600.0])
            .with_transparent(true)
            .with_fullsize_content_view(true)
            .with_title_shown(false)
            .with_titlebar_shown(false)
            .with_app_id("com.obedience.voice-agent"),
        ..Default::default()
    };
    let result = eframe::run_native(
        "Obey Voice Agent",
        options,
        Box::new(|cc| Ok(Box::new(app::VoiceApp::new(cc)))),
    );
    if let Err(error) = &result {
        diagnostics::record(
            "error",
            "desktop.failure",
            serde_json::json!({"message":error.to_string()}),
        );
    }
    result
}
