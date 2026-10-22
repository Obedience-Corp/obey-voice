//! Exercise the app event path without an engine, tray service, or personal config.
use super::VoiceApp;
use crate::backend::Event;
use crate::model::{Purpose, Screen};
use crate::tray::Action;
use std::sync::mpsc;

#[test]
fn engine_loss_during_pause_does_not_disable_tray_after_reconnect() {
    // Demo mode is read during construction. Keep that environment change in a
    // subprocess so parallel tests never inherit it.
    if std::env::var_os("OBEY_TRAY_RECONNECT_TEST").is_none() {
        assert!(
            std::process::Command::new(std::env::current_exe().unwrap())
                .args([
                    "--exact",
                    "app::resident_tests::engine_loss_during_pause_does_not_disable_tray_after_reconnect",
                ])
                .env("OBEY_TRAY_RECONNECT_TEST", "1")
                .env("OBEY_VOICE_AGENT_DEMO", "1")
                .status()
                .unwrap()
                .success()
        );
        return;
    }

    let cc = eframe::CreationContext::_new_kittest(Default::default());
    let mut app = VoiceApp::new(&cc);
    let (tx, events) = mpsc::channel();
    app.backend.events = events;
    app.backend.generation = 2;
    app.screen = Screen::Meeting;
    app.meeting_active = true;
    app.pending.insert(41, Purpose::MeetingPaused(true));
    app.pending.insert(42, Purpose::Task);

    tx.send(Event::Lost(1, "previous engine exited".into()))
        .unwrap();
    app.poll();
    assert!(app.connected);
    assert!(app.meeting_active);
    assert!(app.pending.contains_key(&41));
    assert!(!app.tray_snapshot().enabled(Action::Microphone));

    tx.send(Event::Lost(2, "engine exited during pause".into()))
        .unwrap();
    app.poll();
    assert!(!app.connected);
    assert!(!app.meeting_active);
    assert!(
        app.pending.contains_key(&42),
        "independent CLI job survives"
    );

    app.backend.generation = 3;
    tx.send(Event::Bridge(3, serde_json::json!({"event":"ready"})))
        .unwrap();
    app.poll();
    let state = app.tray_snapshot();
    assert!(state.connected);
    for action in [Action::Conversation, Action::Meeting, Action::Settings] {
        assert!(
            state.enabled(action),
            "{action:?} remains disabled after reconnect"
        );
    }
}
