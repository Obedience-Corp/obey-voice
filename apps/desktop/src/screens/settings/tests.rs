use crate::app::VoiceApp;
use crate::model::{KeySpec, Screen};
use eframe::egui::{self, Event, RawInput, Rect, Vec2, epaint::Shape};
use serde_json::json;
use std::collections::HashSet;

#[test]
fn short_window_scrolls_past_catalog_to_all_route_fields() {
    // Isolate demo mode from other tests and never connect to personal config.
    if std::env::var_os("VOICE_SETTINGS_SCROLL_TEST").is_none() {
        assert!(
            std::process::Command::new(std::env::current_exe().unwrap())
                .args([
                    "--exact",
                    "screens::settings::tests::short_window_scrolls_past_catalog_to_all_route_fields",
                ])
                .env("VOICE_SETTINGS_SCROLL_TEST", "1")
                .env("OBEY_VOICE_AGENT_DEMO", "1")
                .status()
                .unwrap()
                .success()
        );
        return;
    }
    let cc = eframe::CreationContext::_new_kittest(Default::default());
    let mut app = VoiceApp::new(&cc);
    app.screen = Screen::Settings;
    app.group = "brain".into();
    let fields = [
        "obey_provider",
        "obey_model",
        "obey_socket",
        "obey_tool_policy",
    ];
    app.schema = fields
        .iter()
        .map(|key| KeySpec {
            key: (*key).into(),
            title: format!("Route field {key}"),
            kind: "string".into(),
            editable: true,
            ..Default::default()
        })
        .collect();
    app.values = json!({"brain_provider":{"value":"obey"}});
    for key in fields {
        app.values[key] = json!({"value":format!("saved-{key}"), "source":"config"});
    }
    app.obey_catalog = json!({
        "problem": "Daemon discovery unavailable. ".repeat(20),
        "readiness": {"checks": (0..5).map(|i| json!({
            "stage":format!("stage-{i}"), "state":"unknown", "code":"unavailable",
            "remediation":"Check the selected socket, provider credentials, model configuration and daemon status. ".repeat(8),
        })).collect::<Vec<_>>()},
    });
    let saved = app.values.clone();
    let viewport = Rect::from_min_size(Default::default(), Vec2::new(700., 360.));
    let mut seen = HashSet::new();
    for frame in 0..160 {
        let mut events = vec![Event::PointerMoved(egui::pos2(500., 280.))];
        if frame > 1 {
            events.push(Event::MouseWheel {
                unit: egui::MouseWheelUnit::Point,
                delta: Vec2::new(0., -60.),
                phase: egui::TouchPhase::Move,
                source: egui::MouseWheelSource::Unknown,
                modifiers: Default::default(),
            });
        }
        let mut output = cc.egui_ctx.run_ui(
            RawInput {
                screen_rect: Some(viewport),
                time: Some(frame as f64 / 30.),
                events,
                ..Default::default()
            },
            |ui| app.settings(ui),
        );
        output.textures_delta.clear(); // This headless harness inspects layout, not GPU textures.
        for clipped in output.shapes {
            visible_text(
                &clipped.shape,
                clipped.clip_rect.intersect(viewport),
                &mut seen,
            );
        }
    }
    for key in fields {
        assert!(
            seen.contains(&format!("Route field {key}")),
            "could not scroll to {key}"
        );
        assert!(
            seen.contains(&format!("saved-{key}")),
            "editor for {key} is clipped"
        );
    }
    assert_eq!(app.values, saved, "scrolling must not change saved choices");
}

fn visible_text(shape: &Shape, clip: Rect, seen: &mut HashSet<String>) {
    match shape {
        Shape::Vec(shapes) => {
            for shape in shapes {
                visible_text(shape, clip, seen);
            }
        }
        Shape::Text(text) if clip.contains_rect(text.visual_bounding_rect()) => {
            seen.insert(text.galley.text().to_owned());
        }
        _ => {}
    }
}
