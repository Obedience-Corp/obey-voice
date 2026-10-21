mod app;
mod backend;
mod diagnostics;
mod model;
mod screens;
mod setup;
mod theme;

fn main() -> eframe::Result {
    let _diagnostics = diagnostics::init();
    let fixture_root = std::env::var_os("OBEY_VOICE_TEST_CONFIG")
        .or_else(|| std::env::var_os("OBEY_VOICE_EVENT_FIXTURE"));
    let options = eframe::NativeOptions {
        persistence_path: fixture_root
            .map(|root| std::path::PathBuf::from(root).join("desktop-ui.ron")),
        viewport: eframe::egui::ViewportBuilder::default()
            .with_icon(
                eframe::icon_data::from_png_bytes(include_bytes!(
                    "../assets/AppIcon.iconset/icon_512x512.png"
                ))
                .expect("the bundled Obedience Corp icon must be a valid PNG"),
            )
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
