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
    app.pending.insert(43, Purpose::MeetingStop);
    app.pending.insert(44, Purpose::MeetingStart);

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
    for id in [41, 43, 44] {
        assert!(
            !app.pending.contains_key(&id),
            "lost engine left meeting control {id} pending"
        );
    }
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

#[test]
fn automatic_meeting_stop_is_single_flight_even_during_startup() {
    if std::env::var_os("OBEY_MEETING_STOP_TEST").is_none() {
        assert!(std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "app::resident_tests::automatic_meeting_stop_is_single_flight_even_during_startup"])
            .env("OBEY_MEETING_STOP_TEST", "1")
            .env("OBEY_VOICE_AGENT_DEMO", "1")
            .status().unwrap().success());
        return;
    }
    let cc = eframe::CreationContext::_new_kittest(Default::default());
    for during_start in [false, true] {
        let mut app = VoiceApp::new(&cc);
        let (tx, events) = mpsc::channel();
        app.backend.events = events;
        app.screen = Screen::Meeting;
        app.demo = false; // Backend remains inert; exercise real pending-request bookkeeping.
        app.meeting_active = !during_start;
        if during_start {
            app.starting = true;
            app.pending.insert(900, Purpose::MeetingStart);
        }
        let stop = || Event::Bridge(0, serde_json::json!({"event":"meeting_stop_requested"}));
        tx.send(stop()).unwrap();
        app.poll();
        assert!(
            app.starting,
            "automatic stop must lock recording controls while saving"
        );
        if during_start {
            tx.send(Event::Bridge(
                0,
                serde_json::json!({"id":900,"result":{"path":"test.meeting"}}),
            ))
            .unwrap();
            app.poll();
            assert!(
                app.starting,
                "late start acknowledgement must not unlock saving controls"
            );
        }
        tx.send(stop()).unwrap();
        app.poll();
        app.stop_meeting(); // A queued screen/tray action must not send a second stop.
        let stops: Vec<_> = app
            .pending
            .iter()
            .filter_map(|(id, p)| matches!(p, Purpose::MeetingStop).then_some(*id))
            .collect();
        assert_eq!(
            stops.len(),
            1,
            "duplicate stop would turn a saved meeting into an error"
        );
        assert!(!app.tray_snapshot().enabled(Action::Meeting));
        assert!(!app.tray_snapshot().enabled(Action::Microphone));
        app.demo = true; // Suppress the unrelated meeting-list CLI after acknowledgement.
        tx.send(Event::Bridge(
            0,
            serde_json::json!({"id":stops[0],"result":{"path":"test.meeting"}}),
        ))
        .unwrap();
        app.poll();
        assert!(!app.starting);
        assert!(!app.meeting_active);
        assert!(app.error.is_none());
        assert_eq!(app.meeting_result["path"], "test.meeting");
        app.demo = false;
        tx.send(stop()).unwrap();
        app.poll();
        assert!(
            app.pending
                .values()
                .all(|p| !matches!(p, Purpose::MeetingStop)),
            "late stop event must not stop an already saved meeting"
        );
    }
}
