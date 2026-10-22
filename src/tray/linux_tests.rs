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
    BufReader::new(watcher.0.stdout.take().unwrap())
        .read_line(&mut ready)
        .unwrap();
    assert_eq!(ready.trim(), "ready");
    let (tx, _rx) = mpsc::channel();
    let dispatch = Dispatch {
        tx,
        ctx: Default::default(),
        available: Arc::default(),
    };
    let tray = Tray::new(dispatch.clone(), Snapshot::default()).unwrap();
    std::thread::sleep(Duration::from_millis(1400));
    assert!(
        !dispatch.available.load(Ordering::Acquire),
        "watcher alone must not enable close-to-tray"
    );
    writeln!(watcher.0.stdin.as_mut().unwrap(), "online").unwrap();
    wait_for(&dispatch, true);
    writeln!(watcher.0.stdin.as_mut().unwrap(), "offline").unwrap();
    wait_for(&dispatch, false);
    writeln!(watcher.0.stdin.as_mut().unwrap(), "online").unwrap();
    wait_for(&dispatch, true);
    drop(watcher);
    wait_for(&dispatch, false);
    drop(tray);
}
