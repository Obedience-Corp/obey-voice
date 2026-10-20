use serde_json::{Value, json};
use std::{
    collections::{HashMap, HashSet},
    io::{BufRead, BufReader, Read, Write},
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
        mpsc,
    },
    thread,
    time::{Duration, Instant},
};

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
    bridge: Option<Arc<Mutex<Child>>>,
    ctx: eframe::egui::Context,
}
fn spawn_command(path: &PathBuf, args: &[String]) -> std::io::Result<Child> {
    let mut cmd = Command::new(path);
    cmd.args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    cmd.env("NO_COLOR", "1").env("TERM", "dumb");
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }
    cmd.spawn()
}
fn signal_child(child: &mut Child) {
    #[cfg(unix)]
    // Each owned command has a new process group. Only signal that group.
    unsafe {
        libc::kill(-(child.id() as i32), libc::SIGTERM);
    }
    #[cfg(not(unix))]
    let _ = child.kill();
}
fn finish_child(child: Arc<Mutex<Child>>, grace: Duration) {
    thread::spawn(move || {
        let start = Instant::now();
        loop {
            let mut process = child.lock().unwrap();
            if !matches!(process.try_wait(), Ok(None)) {
                break;
            }
            if start.elapsed() > grace {
                #[cfg(unix)]
                unsafe {
                    libc::kill(-(process.id() as i32), libc::SIGKILL);
                }
                let _ = process.kill();
                let _ = process.wait();
                break;
            }
            drop(process);
            thread::sleep(Duration::from_millis(50));
        }
    });
}
fn send(tx: &mpsc::Sender<Event>, ctx: &eframe::egui::Context, event: Event) {
    let _ = tx.send(event);
    ctx.request_repaint();
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
                let mut stdin = child.stdin.take().unwrap();
                let stdout = child.stdout.take().unwrap();
                let stderr = child.stderr.take().unwrap();
                let (tx_input, rx_input) = mpsc::channel::<Value>();
                self.input = Some(tx_input);
                let tx = self.tx.clone();
                let ctx = self.ctx.clone();
                thread::spawn(move || {
                    for value in rx_input {
                        if writeln!(stdin, "{value}").is_err() {
                            break;
                        }
                    }
                });
                thread::spawn(move || {
                    for line in BufReader::new(stdout).lines() {
                        match line {
                            Ok(line) => match serde_json::from_str(&line) {
                                Ok(value) => send(&tx, &ctx, Event::Bridge(generation, value)),
                                Err(e) => send(
                                    &tx,
                                    &ctx,
                                    Event::Lost(
                                        generation,
                                        format!("Invalid engine response: {e}"),
                                    ),
                                ),
                            },
                            Err(e) => {
                                send(&tx, &ctx, Event::Lost(generation, e.to_string()));
                                break;
                            }
                        }
                    }
                    send(
                        &tx,
                        &ctx,
                        Event::Lost(
                            generation,
                            "Voice engine stopped. Return home and reconnect.".into(),
                        ),
                    );
                });
                let tx = self.tx.clone();
                let ctx = self.ctx.clone();
                thread::spawn(move || {
                    for line in BufReader::new(stderr).lines().map_while(Result::ok) {
                        send(&tx, &ctx, Event::Log { id: 0, line });
                    }
                });
                let child = Arc::new(Mutex::new(child));
                self.bridge = Some(child);
            }
        }
    }
    pub fn reset(&mut self) {
        self.input.take();
        if let Some(child) = self.bridge.take() {
            signal_child(&mut child.lock().unwrap());
            finish_child(child, Duration::from_secs(5));
        }
        self.connect();
    }
    pub fn request(&self, value: Value) {
        if self
            .input
            .as_ref()
            .is_none_or(|input| input.send(value).is_err())
        {
            send(
                &self.tx,
                &self.ctx,
                Event::Lost(self.generation, "The voice engine is not connected".into()),
            );
        }
    }
    pub fn cli(&self, id: u64, args: Vec<String>, stream: bool) {
        let engine = self.engine.clone();
        let tx = self.tx.clone();
        let ctx = self.ctx.clone();
        let jobs = self.jobs.clone();
        let stopped = self.stopped.clone();
        let cancelled = self.cancelled.clone();
        thread::spawn(move || {
            let result = (|| -> Result<Value, String> {
                if stopped.load(Ordering::Acquire) || cancelled.lock().unwrap().remove(&id) {
                    return Err("Cancelled".into());
                }
                let mut child = spawn_command(&engine, &args).map_err(|e| e.to_string())?;
                child.stdin.take();
                let stdout = child.stdout.take().unwrap();
                let stderr = child.stderr.take().unwrap();
                let child = Arc::new(Mutex::new(child));
                {
                    let mut registry = jobs.lock().unwrap();
                    registry.insert(id, child.clone());
                    if stopped.load(Ordering::Acquire) || cancelled.lock().unwrap().remove(&id) {
                        signal_child(&mut child.lock().unwrap());
                        finish_child(child.clone(), Duration::from_secs(2));
                    }
                }
                let err_tx = tx.clone();
                let err_ctx = ctx.clone();
                let stderr_thread = thread::spawn(move || {
                    let mut tail = String::new();
                    for line in BufReader::new(stderr).lines().map_while(Result::ok) {
                        if tail.len() < 32768 {
                            tail.push_str(&line);
                            tail.push('\n');
                        }
                        if stream {
                            send(&err_tx, &err_ctx, Event::Log { id, line });
                        }
                    }
                    tail
                });
                let out_tx = tx.clone();
                let out_ctx = ctx.clone();
                let stdout_thread = thread::spawn(move || -> Result<String, String> {
                    let mut output = String::new();
                    if stream {
                        for line in BufReader::new(stdout).lines() {
                            let line = line.map_err(|e| e.to_string())?;
                            if output.len() < 4 << 20 {
                                output.push_str(&line);
                                output.push('\n');
                            }
                            send(&out_tx, &out_ctx, Event::Log { id, line });
                        }
                    } else {
                        stdout
                            .take(16 << 20)
                            .read_to_string(&mut output)
                            .map_err(|e| e.to_string())?;
                    }
                    Ok(output)
                });
                let start = Instant::now();
                let mut timed_out = false;
                let status = loop {
                    if let Some(status) = child
                        .lock()
                        .unwrap()
                        .try_wait()
                        .map_err(|e| e.to_string())?
                    {
                        break status;
                    }
                    if !stream && !timed_out && start.elapsed() > Duration::from_secs(60) {
                        timed_out = true;
                        signal_child(&mut child.lock().unwrap());
                        finish_child(child.clone(), Duration::from_secs(2));
                    }
                    thread::sleep(Duration::from_millis(30));
                };
                jobs.lock().unwrap().remove(&id);
                let stdout = stdout_thread
                    .join()
                    .map_err(|_| "Output reader stopped")??;
                let stderr = stderr_thread.join().unwrap_or_default();
                if timed_out {
                    return Err("The command timed out after 60 seconds".into());
                }
                if !status.success() {
                    return Err((if stdout.trim().is_empty() {
                        stderr.trim()
                    } else {
                        stdout.trim()
                    })
                    .to_string());
                }
                Ok(serde_json::from_str(&stdout).unwrap_or(Value::String(stdout)))
            })();
            send(&tx, &ctx, Event::Result { id, result });
        });
    }
    pub fn cancel(&self, id: u64) {
        self.cancelled.lock().unwrap().insert(id);
        if let Some(child) = self.jobs.lock().unwrap().get(&id).cloned() {
            signal_child(&mut child.lock().unwrap());
            finish_child(child, Duration::from_secs(5));
        }
    }
}
impl Drop for Backend {
    fn drop(&mut self) {
        self.stopped.store(true, Ordering::Release);
        if let Some(input) = self.input.take() {
            let _ = input.send(json!({"method":"shutdown"}));
        }
        let mut children: Vec<_> = self.jobs.lock().unwrap().values().cloned().collect();
        for child in &children {
            signal_child(&mut child.lock().unwrap());
        }
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
