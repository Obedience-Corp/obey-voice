//! Real D-Bus host lifecycle check in an isolated session bus.
use super::*;
use std::io::{BufRead, BufReader, Write};
use std::process::{Child, Command, Stdio};
use std::time::Instant;

struct Watcher(Child);
impl Drop for Watcher {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}
fn wait_for(dispatch: &Dispatch, expected: bool) {
    let deadline = Instant::now() + Duration::from_secs(6);
    while dispatch.available.load(Ordering::Acquire) != expected {
        assert!(
            Instant::now() < deadline,
            "tray availability did not become {expected}"
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}
#[test]
#[ignore = "requires an isolated D-Bus session; run just test linux"]
fn watcher_without_panel_is_not_available_and_panel_loss_is_detected() {
    let mut watcher = Watcher(
        Command::new("python3")
            .arg("scripts/tray-watcher.py")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .unwrap(),
    );
    let mut ready = String::new();
    let mut replies = BufReader::new(watcher.0.stdout.take().unwrap());
    replies.read_line(&mut ready).unwrap();
    assert_eq!(ready.trim(), "ready");
    let (tx, rx) = mpsc::channel();
    let dispatch = Dispatch {
        tx,
        ctx: Default::default(),
        available: Arc::default(),
    };
    let mut tray = Tray::new(dispatch.clone(), Snapshot::default()).unwrap();
    std::thread::sleep(Duration::from_millis(1400));
    assert!(
        !dispatch.available.load(Ordering::Acquire),
        "watcher alone must not enable close-to-tray"
    );
    writeln!(watcher.0.stdin.as_mut().unwrap(), "online").unwrap();
    wait_for(&dispatch, true);
    // Exercise the exported D-Bus API, not just the in-process menu policy.
    let mut menu = |command: &str| -> serde_json::Value {
        writeln!(watcher.0.stdin.as_mut().unwrap(), "{command}").unwrap();
        let mut line = String::new();
        replies.read_line(&mut line).unwrap();
        let rows: serde_json::Value = serde_json::from_str(&line).unwrap();
        assert!(rows.is_array(), "D-Bus menu query failed: {rows}");
        rows
    };
    let rows = menu("inspect");
    let entry = |rows: &serde_json::Value, label: &str| {
        rows.as_array()
            .unwrap()
            .iter()
            .find(|r| r["label"] == label)
            .unwrap()
            .clone()
    };
    assert_eq!(entry(&rows, "New conversation…")["enabled"], false);
    menu("activate");
    assert_eq!(
        rx.recv_timeout(Duration::from_secs(2)).unwrap(),
        Action::Open
    );
    menu("click:Diagnostics…");
    assert_eq!(
        rx.recv_timeout(Duration::from_secs(2)).unwrap(),
        Action::Diagnostics
    );
    tray.update(&Snapshot {
        connected: true,
        meeting: true,
        ..Default::default()
    });
    let deadline = Instant::now() + Duration::from_secs(3);
    let rows = loop {
        let rows = menu("inspect");
        if rows[0]["label"] == "Obey — Recording meeting" {
            break rows;
        }
        assert!(
            Instant::now() < deadline,
            "updated meeting menu not exported"
        );
        std::thread::sleep(Duration::from_millis(25));
    };
    assert_eq!(entry(&rows, "Stop and review meeting")["enabled"], true);
    assert_eq!(entry(&rows, "Settings…")["enabled"], false);
    menu("click:Pause meeting");
    assert_eq!(
        rx.recv_timeout(Duration::from_secs(2)).unwrap(),
        Action::Microphone
    );
    menu("click:Stop and review meeting");
    assert_eq!(
        rx.recv_timeout(Duration::from_secs(2)).unwrap(),
        Action::Meeting
    );
    writeln!(watcher.0.stdin.as_mut().unwrap(), "offline").unwrap();
    wait_for(&dispatch, false);
    writeln!(watcher.0.stdin.as_mut().unwrap(), "online").unwrap();
    wait_for(&dispatch, true);
    drop(watcher);
    wait_for(&dispatch, false);
    drop(tray);
}
