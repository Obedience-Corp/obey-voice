//! Engine connection and ownership; subprocess I/O stays off the UI thread.
mod commands;
mod directory;
mod process;
mod protocol;
#[cfg(test)]
mod tests;

#[cfg(test)]
pub(crate) use self::process::finishes_on_its_own;
use self::process::{finish_child, signal_child, spawn_command};
use self::protocol::{read_protocol, send, supervise_bridge};
use serde_json::{Value, json};
use std::collections::{HashMap, HashSet};
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::Child;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, mpsc};
use std::thread;
use std::time::{Duration, Instant};

#[derive(Debug)]
pub enum Event {
    Bridge(u64, Value),
    Result {
        id: u64,
        result: Result<Value, String>,
    },
    Log {
        id: u64,
        line: String,
    },
    Lost(u64, String),
    ProtocolWarning(u64, String),
}

pub struct Backend {
    pub events: mpsc::Receiver<Event>,
    tx: mpsc::Sender<Event>,
    input: Option<mpsc::Sender<Value>>,
    engine: PathBuf,
    pub generation: u64,
    stopped: Arc<AtomicBool>,
    cancelled: Arc<Mutex<HashSet<u64>>>,
    jobs: Arc<Mutex<HashMap<u64, Arc<Mutex<Child>>>>>,
    /// Jobs that are signalled once on Cancel or quit and never killed.
    gentle: Arc<Mutex<HashSet<u64>>>,
    signalled: Arc<Mutex<HashSet<u64>>>,
    bridge: Option<Arc<Mutex<Child>>>,
    ctx: eframe::egui::Context,
}
impl Backend {
    pub fn new(ctx: eframe::egui::Context, demo: bool) -> Self {
        let (tx, events) = mpsc::channel();
        let engine = std::env::var_os("OBEY_VOICE_ENGINE")
            .map(PathBuf::from)
            .unwrap_or_else(|| {
                let exe = std::env::current_exe().unwrap_or_default();
                let adjacent = exe.with_file_name("obey-voice-engine");
                if adjacent.is_file() {
                    adjacent
                } else {
                    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("bin/obey-voice-engine")
                }
            });
        let mut this = Self {
            events,
            tx,
            input: None,
            engine,
            generation: 0,
            stopped: Arc::default(),
            cancelled: Arc::default(),
            jobs: Arc::default(),
            gentle: Arc::default(),
            signalled: Arc::default(),
            bridge: None,
            ctx,
        };
        if !demo {
            this.connect();
        }
        this
    }
    pub fn connect(&mut self) {
        if self.bridge.is_some() {
            return;
        }
        self.generation += 1;
        let generation = self.generation;
        crate::diagnostics::record(
            "info",
            "engine.spawn",
            json!({"generation":generation,"executable":self.engine}),
        );
        match spawn_command(&self.engine, &["--bridge".into()]) {
            Err(e) => send(
                &self.tx,
                &self.ctx,
                Event::Lost(
                    generation,
                    format!(
                        "Cannot start {}: {e}. Run just build or set OBEY_VOICE_ENGINE.",
                        self.engine.display()
                    ),
                ),
            ),
            Ok(mut child) => {
                crate::diagnostics::record(
                    "info",
                    "engine.started",
                    json!({"generation":generation,"pid":child.id()}),
                );
                let mut stdin = child.stdin.take().unwrap();
                let stdout = child.stdout.take().unwrap();
                let stderr = child.stderr.take().unwrap();
                let child = Arc::new(Mutex::new(child));
                let failed = Arc::new(AtomicBool::new(false));
                let (tx_input, rx_input) = mpsc::channel::<Value>();
                self.input = Some(tx_input);
                let writer_failed = failed.clone();
                thread::spawn(move || {
                    for value in rx_input {
                        if writeln!(stdin, "{value}").is_err() {
                            writer_failed.store(true, Ordering::Release);
                            break;
                        }
                    }
                });
                let tx = self.tx.clone();
                let ctx = self.ctx.clone();
                let reader_failed = failed.clone();
                thread::spawn(move || {
                    read_protocol(BufReader::new(stdout), generation, &tx, &ctx);
                    reader_failed.store(true, Ordering::Release);
                });
                let tx = self.tx.clone();
                let ctx = self.ctx.clone();
                let process = child.clone();
                thread::spawn(move || {
                    supervise_bridge(process, failed, generation, &tx, &ctx);
                });
                let tx = self.tx.clone();
                let ctx = self.ctx.clone();
                thread::spawn(move || {
                    for line in BufReader::new(stderr).lines().map_while(Result::ok) {
                        crate::diagnostics::record(
                            "info",
                            "engine.stderr",
                            json!({"generation":generation,"message":line}),
                        );
                        send(&tx, &ctx, Event::Log { id: 0, line });
                    }
                });
                self.bridge = Some(child);
            }
        }
    }
    pub fn reset(&mut self) {
        // Reconnect is available only after the supervisor has reaped the
        // previous engine. Never interrupt a still-running session or save.
        if let Some(child) = &self.bridge
            && !matches!(child.lock().unwrap().try_wait(), Ok(Some(_)))
        {
            return;
        }
        self.input.take();
        self.bridge.take();
        self.connect();
    }
    pub fn request(&self, value: Value) {
        crate::diagnostics::record(
            "trace",
            "engine.send",
            json!({"generation":self.generation,"message":crate::diagnostics::protocol_summary(&value)}),
        );
        if self
            .input
            .as_ref()
            .is_none_or(|input| input.send(value).is_err())
        {
            send(
                &self.tx,
                &self.ctx,
                Event::ProtocolWarning(self.generation, "The voice engine is not connected".into()),
            );
        }
    }
    pub fn cancel(&self, id: u64) {
        self.cancelled.lock().unwrap().insert(id);
        if let Some(child) = self.jobs.lock().unwrap().get(&id).cloned() {
            if self.gentle.lock().unwrap().contains(&id) {
                if self.signalled.lock().unwrap().insert(id) {
                    signal_child(&mut child.lock().unwrap());
                }
                return;
            }
            signal_child(&mut child.lock().unwrap());
            finish_child(child, Duration::from_secs(5));
        }
    }
    /// Whether Cancel was sent to a job that finishes its own cleanup.
    pub fn cleaning_up(&self, id: u64) -> bool {
        self.signalled.lock().unwrap().contains(&id)
    }
}
impl Drop for Backend {
    fn drop(&mut self) {
        self.stopped.store(true, Ordering::Release);
        if let Some(input) = self.input.take() {
            let _ = input.send(json!({"method":"shutdown"}));
        }
        let gentle = self.gentle.lock().unwrap().clone();
        let mut signalled = self.signalled.lock().unwrap();
        let mut children = Vec::new();
        for (id, child) in self.jobs.lock().unwrap().iter() {
            // A model move is asked to stop once and left to finish its
            // cleanup after the app has gone; it ignores the closed pipe.
            if gentle.contains(id) {
                if signalled.insert(*id) {
                    signal_child(&mut child.lock().unwrap());
                }
                continue;
            }
            signal_child(&mut child.lock().unwrap());
            children.push(child.clone());
        }
        drop(signalled);
        if let Some(child) = self.bridge.take() {
            children.push(child);
        }
        let deadline = Instant::now() + Duration::from_secs(12);
        for child in children {
            loop {
                let mut p = child.lock().unwrap();
                if !matches!(p.try_wait(), Ok(None)) {
                    break;
                }
                if Instant::now() >= deadline {
                    #[cfg(unix)]
                    unsafe {
                        libc::kill(-(p.id() as i32), libc::SIGKILL);
                    }
                    let _ = p.kill();
                    let _ = p.wait();
                    break;
                }
                drop(p);
                thread::sleep(Duration::from_millis(20));
            }
        }
    }
}
