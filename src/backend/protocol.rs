use super::Event;
use super::process::signal_child;
use serde_json::json;
use std::io::BufRead;
use std::process::Child;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, mpsc};
use std::thread;
use std::time::{Duration, Instant};

pub(super) fn send(tx: &mpsc::Sender<Event>, ctx: &eframe::egui::Context, event: Event) {
    use crate::diagnostics::{protocol_summary, record};
    match &event {
        Event::Bridge(generation, v) => record(
            if v.get("error").is_some() || v["event"] == "error" {
                "error"
            } else {
                "trace"
            },
            "engine.receive",
            json!({"generation":generation,"message":protocol_summary(v)}),
        ),
        Event::Lost(generation, error) | Event::ProtocolWarning(generation, error) => record(
            "error",
            "engine.failure",
            json!({"generation":generation,"message":error}),
        ),
        Event::Result { id, result } => record(
            if result.is_err() { "error" } else { "info" },
            "command.finish",
            json!({"id":id,"error":result.as_ref().err()}),
        ),
        // Streamed stdout can include remote credentials. Only stderr is persisted separately.
        Event::Log { .. } => {}
    }
    let _ = tx.send(event);
    ctx.request_repaint();
}

// A diagnostic line is recoverable: keep reading snapshots and leave the
// conversation active. Only the process supervisor may report session death.
pub(super) fn read_protocol(
    reader: impl BufRead,
    generation: u64,
    tx: &mpsc::Sender<Event>,
    ctx: &eframe::egui::Context,
) {
    for line in reader.lines() {
        let event = match line {
            Ok(line) => match serde_json::from_str(&line) {
                Ok(value) => Event::Bridge(generation, value),
                Err(e) => {
                    Event::ProtocolWarning(generation, format!("Invalid engine response: {e}"))
                }
            },
            Err(e) => {
                send(tx, ctx, Event::ProtocolWarning(generation, e.to_string()));
                break;
            }
        };
        send(tx, ctx, event);
    }
}

pub(super) fn supervise_bridge(
    child: Arc<Mutex<Child>>,
    failed: Arc<AtomicBool>,
    generation: u64,
    tx: &mpsc::Sender<Event>,
    ctx: &eframe::egui::Context,
) {
    let mut stopping = None;
    loop {
        let mut process = child.lock().unwrap();
        match process.try_wait() {
            Ok(Some(status)) => {
                send(
                    tx,
                    ctx,
                    Event::Lost(
                        generation,
                        format!("Voice engine stopped ({status}). Return home and reconnect."),
                    ),
                );
                return;
            }
            Err(e) => {
                // Reap before marking the session dead, even on a wait error.
                let _ = process.kill();
                if process.wait().is_ok() {
                    send(tx, ctx, Event::Lost(generation, e.to_string()));
                    return;
                }
            }
            Ok(None) => {}
        }
        if failed.load(Ordering::Acquire) && stopping.is_none() {
            signal_child(&mut process);
            stopping = Some(Instant::now());
        }
        if stopping.is_some_and(|start| start.elapsed() >= Duration::from_secs(12)) {
            #[cfg(unix)]
            unsafe {
                libc::kill(-(process.id() as i32), libc::SIGKILL);
            }
            let _ = process.kill();
        }
        drop(process);
        thread::sleep(Duration::from_millis(20));
    }
}
