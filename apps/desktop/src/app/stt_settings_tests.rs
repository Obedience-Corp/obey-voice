//! Effective engine sources must control model selection, including late replies.
use super::VoiceApp;
use crate::backend::Event;
use crate::model::Purpose;
use serde_json::json;
use std::sync::mpsc;

#[test]
fn environment_model_file_blocks_desktop_save_reset_install_and_late_save() {
    if std::env::var_os("OBEY_STT_OVERRIDE_TEST").is_none() {
        assert!(std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "app::stt_settings_tests::environment_model_file_blocks_desktop_save_reset_install_and_late_save"])
            .env("OBEY_STT_OVERRIDE_TEST", "1")
            .env("OBEY_VOICE_AGENT_DEMO", "1")
            .status().unwrap().success());
        return;
    }
    let cc = eframe::CreationContext::_new_kittest(Default::default());
    let mut app = VoiceApp::new(&cc);
    app.values = json!({
        "stt_provider":{"value":"whispercpp","source":"file"},
        "whispercpp_model":{"value":"base.en","source":"file"},
        "whispercpp_model_path":{"value":"/external/forced-model.bin","source":"env"},
    });
    app.drafts
        .insert("whispercpp_model".into(), "small.en".into());
    let before = app.values.clone();
    let next = app.next_id;
    // Both setup and settings editors use this guarded write action.
    for value in [Some("small.en".into()), None] {
        app.save_setting_choice("whispercpp_model", value);
        assert_eq!(app.next_id, next, "a refused edit issued a CLI write");
        assert_eq!(app.values, before);
        assert!(
            app.error
                .as_ref()
                .unwrap()
                .contains("WHISPERCPP_MODEL_PATH")
        );
        assert!(
            app.error
                .as_ref()
                .unwrap()
                .contains("/external/forced-model.bin")
        );
    }
    app.start_stt_install();
    assert!(app.task.is_none());
    assert_eq!(app.next_id, next, "repair issued a managed download");
    // Even an already queued save completion must not clear the forced path.
    let (tx, events) = mpsc::channel();
    app.backend.events = events;
    app.error = None;
    app.pending.insert(910, Purpose::STTModelSave);
    tx.send(Event::Result {
        id: 910,
        result: Ok(json!({"value":"small.en"})),
    })
    .unwrap();
    app.poll();
    assert!(
        app.error
            .as_ref()
            .unwrap()
            .contains("WHISPERCPP_MODEL_PATH")
    );
    assert!(app.task.is_none());
    assert_eq!(app.values, before);
    assert!(
        app.task_title.is_empty(),
        "late completion started an installation"
    );

    // A file/default-sourced path is a saved override that selection may clear.
    for source in ["file", "default"] {
        app.values["whispercpp_model_path"]["source"] = json!(source);
        app.error = None;
        let next = app.next_id;
        app.save_setting_choice("whispercpp_model", Some("small.en".into()));
        assert!(app.error.is_none());
        assert_eq!(
            app.next_id,
            next + 1,
            "saved override blocked managed choice"
        );
    }
}

#[test]
fn stt_install_waits_for_effective_settings_after_provider_save() {
    if std::env::var_os("OBEY_STT_REFRESH_TEST").is_none() {
        assert!(std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "app::stt_settings_tests::stt_install_waits_for_effective_settings_after_provider_save"])
            .env("OBEY_STT_REFRESH_TEST", "1")
            .env("OBEY_VOICE_AGENT_DEMO", "1")
            .status().unwrap().success());
        return;
    }
    let cc = eframe::CreationContext::_new_kittest(Default::default());
    let mut app = VoiceApp::new(&cc);
    let (tx, events) = mpsc::channel();
    app.backend.events = events;
    app.values = json!({"stt_provider":{"value":"parakeet","source":"file"}});
    app.pending
        .insert(911, Purpose::SettingSave("stt_provider".into()));
    tx.send(Event::Result {
        id: 911,
        result: Ok(json!({})),
    })
    .unwrap();
    app.poll();
    assert!(
        app.task_title.is_empty(),
        "installed before refreshing effective settings"
    );
    app.pending.insert(912, Purpose::STTSettingsValues);
    tx.send(Event::Result {
        id: 912,
        result: Ok(json!({"values":{
            "stt_provider":{"value":"whispercpp","source":"file"},
            "whispercpp_model_path":{"value":"/external/forced-model.bin","source":"env"}
        }})),
    })
    .unwrap();
    app.poll();
    assert!(app.task_title.is_empty());
    assert!(
        app.error
            .as_ref()
            .unwrap()
            .contains("WHISPERCPP_MODEL_PATH")
    );
}
