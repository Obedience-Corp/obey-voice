//! Local, bounded diagnostics. The UI enqueues records; only the writer touches disk.
use serde_json::{Value, json};
use std::{
    collections::VecDeque,
    fs::{File, OpenOptions},
    io::{self, Write},
    path::PathBuf,
    sync::{
        Arc, Mutex, OnceLock,
        atomic::{AtomicU64, Ordering},
        mpsc,
    },
    thread,
    time::{SystemTime, UNIX_EPOCH},
};

const LIMIT: u64 = 5 * 1024 * 1024;
static LOG: OnceLock<Diagnostics> = OnceLock::new();
struct Diagnostics {
    tx: mpsc::SyncSender<Option<String>>,
    state: Arc<Mutex<Snapshot>>,
    dropped: AtomicU64,
}
#[derive(Clone, Default)]
pub struct Snapshot {
    pub path: PathBuf,
    pub error: Option<String>,
    pub lines: VecDeque<String>,
}
pub struct Guard(Option<thread::JoinHandle<()>>);
impl Drop for Guard {
    fn drop(&mut self) {
        record("info", "desktop.stop", json!({}));
        if let Some(log) = LOG.get() {
            let _ = log.tx.send(None);
        }
        if let Some(handle) = self.0.take() {
            let _ = handle.join();
        }
    }
}
pub fn directory() -> PathBuf {
    if let Some(path) = std::env::var_os("OBEY_VOICE_LOG_DIR") {
        return path.into();
    }
    let home = std::env::home_dir().unwrap_or_else(std::env::temp_dir);
    let base = if cfg!(target_os = "macos") {
        home.join("Library/Logs")
    } else if cfg!(target_os = "windows") {
        std::env::var_os("LOCALAPPDATA")
            .map(PathBuf::from)
            .unwrap_or(home)
    } else {
        std::env::var_os("XDG_STATE_HOME")
            .map(PathBuf::from)
            .unwrap_or(home.join(".local/state"))
    };
    let demo = std::env::args().any(|a| a == "--demo")
        || std::env::var_os("OBEY_VOICE_AGENT_DEMO").is_some();
    base.join(if demo {
        "obey-voice-agent-demo"
    } else {
        "obey-voice-agent"
    })
}
fn open(path: &PathBuf) -> io::Result<File> {
    let mut options = OpenOptions::new();
    options.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)
}
struct RustLogger;
impl log::Log for RustLogger {
    fn enabled(&self, metadata: &log::Metadata<'_>) -> bool {
        metadata.level() <= log::Level::Warn
            || (std::env::var_os("OBEY_VOICE_TRACE_UI").is_some()
                && metadata.target().starts_with("eframe"))
    }
    fn log(&self, entry: &log::Record<'_>) {
        if self.enabled(entry.metadata()) {
            record(
                &entry.level().to_string().to_lowercase(),
                entry.target(),
                json!({"message":entry.args().to_string()}),
            );
        }
    }
    fn flush(&self) {}
}
static RUST_LOGGER: RustLogger = RustLogger;

pub fn init() -> Guard {
    let dir = directory();
    let stamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis();
    let path = dir.join(format!("run-{stamp}-{}.jsonl", std::process::id()));
    let state = Arc::new(Mutex::new(Snapshot {
        path: path.clone(),
        ..Default::default()
    }));
    let (tx, rx) = mpsc::sync_channel::<Option<String>>(1024);
    let writer_state = state.clone();
    let worker = thread::spawn(move || {
        let mut file = std::fs::create_dir_all(&dir).and_then(|()| {
            // Keep the newest ten run files (including rotated segments).
            let mut files: Vec<_> = std::fs::read_dir(&dir)?
                .flatten()
                .map(|e| e.path())
                .filter(|p| {
                    p.file_name()
                        .is_some_and(|n| n.to_string_lossy().starts_with("run-"))
                        && p.extension().is_some_and(|x| x == "jsonl")
                })
                .collect();
            files.sort();
            for old in files.iter().take(files.len().saturating_sub(8)) {
                let _ = std::fs::remove_file(old);
            }
            open(&path)
        });
        let mut size = 0;
        if let Err(err) = &file {
            writer_state.lock().unwrap().error = Some(err.to_string());
        }
        while let Ok(Some(line)) = rx.recv() {
            if size + line.len() as u64 > LIMIT && file.is_ok() {
                // Close before rename for Windows, retaining one previous segment.
                drop(file);
                let previous = path.with_extension("previous.jsonl");
                file = (|| {
                    if previous.exists() {
                        std::fs::remove_file(&previous)?;
                    }
                    std::fs::rename(&path, previous)?;
                    open(&path)
                })();
                size = 0;
            }
            if let Ok(file) = file.as_mut() {
                if let Err(err) = writeln!(file, "{line}") {
                    writer_state.lock().unwrap().error = Some(err.to_string());
                }
                size += line.len() as u64 + 1;
            } else if let Err(err) = &file {
                writer_state.lock().unwrap().error = Some(err.to_string());
            }
        }
    });
    let _ = LOG.set(Diagnostics {
        tx,
        state,
        dropped: AtomicU64::new(0),
    });
    let _ = log::set_logger(&RUST_LOGGER);
    log::set_max_level(log::LevelFilter::Trace);
    let previous = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        record(
            "error",
            "desktop.panic",
            json!({"message": info.to_string()}),
        );
        previous(info);
    }));
    record(
        "info",
        "desktop.start",
        json!({"version": env!("CARGO_PKG_VERSION"), "pid": std::process::id(), "os": std::env::consts::OS}),
    );
    Guard(Some(worker))
}
fn bound_strings(value: &mut Value) {
    match value {
        Value::String(text) if text.len() > 16_384 => {
            let mut end = 16_384;
            while !text.is_char_boundary(end) {
                end -= 1;
            }
            text.truncate(end);
            text.push_str("… [truncated]");
        }
        Value::Array(values) => {
            values.truncate(64);
            for value in values {
                bound_strings(value);
            }
        }
        Value::Object(values) => {
            for value in values.values_mut() {
                bound_strings(value);
            }
        }
        _ => {}
    }
}
pub fn record(level: &str, event: &str, mut data: Value) {
    let Some(log) = LOG.get() else {
        return;
    };
    bound_strings(&mut data);
    let dropped = log.dropped.swap(0, Ordering::Relaxed);
    let line = json!({"timestamp_ms": SystemTime::now().duration_since(UNIX_EPOCH).unwrap_or_default().as_millis(),
        "level": level, "event": event, "data": data,
        "dropped": dropped}).to_string();
    {
        let mut state = log.state.lock().unwrap();
        if state.lines.len() >= 500 {
            state.lines.pop_front();
        }
        state.lines.push_back(line.clone());
    }
    if log.tx.try_send(Some(line)).is_err() {
        log.dropped.fetch_add(dropped + 1, Ordering::Relaxed);
    }
}
pub fn snapshot() -> Snapshot {
    LOG.get()
        .map(|log| log.state.lock().unwrap().clone())
        .unwrap_or_default()
}

/// Deliberately omit transcript, prompt, config values, credentials, and reply bodies.
pub fn protocol_summary(value: &Value) -> Value {
    let mut summary = json!({});
    for key in ["id", "method", "event", "error"] {
        if let Some(v) = value.get(key) {
            summary[key] = v.clone();
        }
    }
    match value["event"].as_str() {
        Some("error" | "progress" | "meeting_phase") => summary["message"] = value["data"].clone(),
        Some("state") => {
            for key in ["status", "error", "mic", "muted", "busy", "voice_available"] {
                summary[key] = value["data"][key].clone();
            }
            summary["turn_count"] = json!(value["data"]["turns"].as_array().map_or(0, Vec::len));
            summary["activity"] = json!(
                value["data"]["activity"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .map(|a| json!({"stage":a["stage"], "milliseconds":a["milliseconds"]}))
                    .collect::<Vec<_>>()
            );
        }
        _ => {}
    }
    summary
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn trace_keeps_correlation_without_conversation_or_config_payloads() {
        let request = protocol_summary(
            &json!({"id":7,"method":"text","text":"private","config":{"secret":"token"}}),
        );
        assert_eq!(request, json!({"id":7,"method":"text"}));
        let state = protocol_summary(
            &json!({"event":"state","data":{"turns":[{"content":"private"}],"response":"private","busy":true,"activity":[{"stage":"tts","detail":"private","milliseconds":12}]}}),
        );
        assert_eq!(state["turn_count"], 1);
        assert_eq!(state["busy"], true);
        assert!(!state.to_string().contains("private"));
    }
    #[test]
    fn persistent_logger_drains_on_exit_and_reports_unwritable_directory() {
        if std::env::var_os("OBEY_LOG_TEST_CHILD").is_some() {
            let guard = init();
            record(
                "error",
                "test.failure",
                json!({"message":"provider unavailable","id":42}),
            );
            drop(guard);
            if std::env::var_os("OBEY_LOG_TEST_BAD_DIR").is_some() {
                assert!(snapshot().error.is_some());
            }
            return;
        }
        let dir = std::env::temp_dir().join(format!("obey-log-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let status = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "diagnostics::tests::persistent_logger_drains_on_exit_and_reports_unwritable_directory"])
            .env("OBEY_LOG_TEST_CHILD", "1").env("OBEY_VOICE_LOG_DIR", &dir).status().unwrap();
        assert!(status.success());
        let path = std::fs::read_dir(&dir)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        let contents = std::fs::read_to_string(path).unwrap();
        assert!(contents.contains("provider unavailable"));
        assert!(contents.contains("desktop.stop"));
        for line in contents.lines() {
            serde_json::from_str::<Value>(line).unwrap();
        }
        let bad = dir.join("file");
        std::fs::write(&bad, "not a directory").unwrap();
        assert!(std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "diagnostics::tests::persistent_logger_drains_on_exit_and_reports_unwritable_directory"])
            .env("OBEY_LOG_TEST_CHILD", "1").env("OBEY_LOG_TEST_BAD_DIR", "1")
            .env("OBEY_VOICE_LOG_DIR", bad).status().unwrap().success());
        std::fs::remove_dir_all(dir).unwrap();
    }
}
