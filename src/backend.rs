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
    bridge: Option<Arc<Mutex<Child>>>,
    ctx: eframe::egui::Context,
}
// Finder does not inherit the user's interactive shell PATH. Keep its entries
// first, then add the standard user/Go/Homebrew installation locations.
fn child_path() -> std::ffi::OsString {
    if let Some(path) = std::env::var_os("OBEY_VOICE_TOOL_PATH") {
        return path;
    }
    let mut paths: Vec<_> =
        std::env::split_paths(&std::env::var_os("PATH").unwrap_or_default()).collect();
    if let Some(home) = std::env::home_dir() {
        for dir in [
            home.join("go/bin"),
            home.join(".local/bin"),
            home.join(".cargo/bin"),
        ] {
            if !paths.contains(&dir) {
                paths.push(dir);
            }
        }
    }
    for dir in ["/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"] {
        let dir = PathBuf::from(dir);
        if !paths.contains(&dir) {
            paths.push(dir);
        }
    }
    std::env::join_paths(paths).unwrap_or_default()
}

fn spawn_command(path: &PathBuf, args: &[String]) -> std::io::Result<Child> {
    let search_path = child_path();
    let executable = if path.components().count() == 1 {
        std::env::split_paths(&search_path)
            .map(|dir| dir.join(path))
            .find(|candidate| {
                #[cfg(unix)]
                {
                    use std::os::unix::fs::PermissionsExt;
                    candidate
                        .metadata()
                        .is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
                }
                #[cfg(not(unix))]
                {
                    candidate.is_file()
                }
            })
            .ok_or_else(|| {
                std::io::Error::new(
                    std::io::ErrorKind::NotFound,
                    format!(
                        "{} is not installed on the tool search path",
                        path.display()
                    ),
                )
            })?
    } else {
        path.clone()
    };
    let mut cmd = Command::new(executable);
    cmd.args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    cmd.env("PATH", search_path)
        .env("NO_COLOR", "1")
        .env("TERM", "dumb");
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
fn read_protocol(
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

fn supervise_bridge(
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
    pub fn cli(&self, id: u64, args: Vec<String>, stream: bool) {
        let campaigns = args == ["--campaigns"];
        let engine = if campaigns {
            PathBuf::from("camp")
        } else {
            self.engine.clone()
        };
        let args = if campaigns {
            vec!["list".into(), "--json".into()]
        } else {
            args
        };
        // Command names only: positional values can contain prompts and secrets.
        crate::diagnostics::record(
            "info",
            "command.start",
            json!({"id":id,"executable":engine,"command":args.first(),"subcommand":args.get(1).filter(|v| !v.starts_with('-'))}),
        );
        let tx = self.tx.clone();
        let ctx = self.ctx.clone();
        let jobs = self.jobs.clone();
        let stopped = self.stopped.clone();
        let cancelled = self.cancelled.clone();
        thread::spawn(move || {
            let started = Instant::now();
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
                        crate::diagnostics::record(
                            "info",
                            "command.stderr",
                            json!({"id":id,"message":line}),
                        );
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
                    if !stream
                        && !timed_out
                        && start.elapsed() > Duration::from_secs(if campaigns { 3 } else { 60 })
                    {
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
                    return Err(if campaigns {
                        "Campaign discovery timed out after 3 seconds; check camp list --json"
                    } else {
                        "The command timed out after 60 seconds"
                    }
                    .into());
                }
                if !status.success() {
                    return Err((if stdout.trim().is_empty() {
                        stderr.trim()
                    } else {
                        stdout.trim()
                    })
                    .to_string());
                }
                if campaigns {
                    let payload = stdout.find('[').map(|i| &stdout[i..]).unwrap_or(&stdout);
                    return serde_json::from_str(payload)
                        .map_err(|e| format!("Cannot read campaign list: {e}"));
                }
                Ok(serde_json::from_str(&stdout).unwrap_or(Value::String(stdout)))
            })();
            crate::diagnostics::record(
                "info",
                "command.duration",
                json!({"id":id,"milliseconds":started.elapsed().as_millis()}),
            );
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

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(unix)]
    #[test]
    fn named_tools_resolve_from_child_search_path() {
        if std::env::var_os("OBEY_TOOL_TEST_CHILD").is_some() {
            let output = spawn_command(&PathBuf::from("camp"), &[])
                .unwrap()
                .wait_with_output()
                .unwrap();
            assert!(output.status.success());
            assert_eq!(
                String::from_utf8(output.stdout).unwrap().trim(),
                "isolated-camp"
            );
            return;
        }
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!("obey-tool-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let camp = dir.join("camp");
        std::fs::write(&camp, "#!/bin/sh\necho isolated-camp\n").unwrap();
        std::fs::set_permissions(&camp, std::fs::Permissions::from_mode(0o700)).unwrap();
        assert!(
            Command::new(std::env::current_exe().unwrap())
                .args([
                    "--exact",
                    "backend::tests::named_tools_resolve_from_child_search_path"
                ])
                .env("OBEY_TOOL_TEST_CHILD", "1")
                .env("PATH", "/usr/bin:/bin")
                .env("OBEY_VOICE_TOOL_PATH", &dir)
                .status()
                .unwrap()
                .success()
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn malformed_protocol_line_preserves_following_snapshots() {
        let (tx, rx) = mpsc::channel();
        let ctx = eframe::egui::Context::default();
        read_protocol(
            &b"{\"event\":\"state\",\"data\":{\"session_id\":\"same\"}}\nnative diagnostic\n{\"event\":\"state\",\"data\":{\"session_id\":\"same\",\"mic\":true}}\n"[..],
            7, &tx, &ctx,
        );
        let events: Vec<_> = rx.try_iter().collect();
        assert_eq!(events.len(), 3);
        assert!(matches!(&events[0], Event::Bridge(7, _)));
        assert!(matches!(&events[1], Event::ProtocolWarning(7, _)));
        assert!(matches!(&events[2], Event::Bridge(7, v) if v["data"]["mic"] == true));
        assert!(!events.iter().any(|e| matches!(e, Event::Lost(..))));
    }

    #[cfg(unix)]
    #[test]
    fn noise_and_reconnect_do_not_interrupt_a_live_engine() {
        use std::os::unix::fs::PermissionsExt;
        let root = std::env::temp_dir().join(format!(
            "obey-engine-test-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir(&root).unwrap();
        let script = root.join("engine");
        std::fs::write(&script, "#!/bin/sh\nprintf '%s\\n' '{\"event\":\"ready\"}' 'native log noise' '{\"event\":\"state\",\"data\":{\"mic\":true}}'\nread request\nprintf '%s\\n' '{\"event\":\"saved\"}'\n").unwrap();
        std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o700)).unwrap();
        let mut backend = Backend::new(eframe::egui::Context::default(), true);
        backend.engine = script;
        backend.connect();
        let mut warning = false;
        loop {
            match backend.events.recv_timeout(Duration::from_secs(3)).unwrap() {
                Event::ProtocolWarning(_, _) => warning = true,
                Event::Bridge(_, v) if v["event"] == "state" => {
                    assert_eq!(v["data"]["mic"], true);
                    break;
                }
                Event::Lost(_, e) => panic!("lost a live engine: {e}"),
                _ => {}
            }
        }
        assert!(warning);
        let generation = backend.generation;
        backend.reset();
        assert_eq!(backend.generation, generation);
        assert!(
            backend
                .bridge
                .as_ref()
                .unwrap()
                .lock()
                .unwrap()
                .try_wait()
                .unwrap()
                .is_none()
        );
        backend.request(json!({"method":"stop"}));
        let mut saved = false;
        let mut exited = false;
        while !saved || !exited {
            match backend.events.recv_timeout(Duration::from_secs(3)).unwrap() {
                Event::Bridge(_, v) if v["event"] == "saved" => saved = true,
                Event::Lost(_, _) => exited = true,
                _ => {}
            }
        }
        drop(backend);
        std::fs::remove_dir_all(root).unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn lost_waits_for_child_exit_after_protocol_eof() {
        use std::os::unix::process::CommandExt;
        // The process closes its protocol then takes time to finish persisting.
        let mut child = Command::new("/bin/sh")
            .args(["-c", "exec 1>&-; sleep 0.3"])
            .stdout(Stdio::piped())
            .process_group(0)
            .spawn()
            .unwrap();
        let stdout = child.stdout.take().unwrap();
        let child = Arc::new(Mutex::new(child));
        let (tx, rx) = mpsc::channel();
        let ctx = eframe::egui::Context::default();
        read_protocol(BufReader::new(stdout), 2, &tx, &ctx);
        assert!(rx.try_recv().is_err());
        assert!(child.lock().unwrap().try_wait().unwrap().is_none());
        let monitor_child = child.clone();
        let monitor = thread::spawn(move || {
            supervise_bridge(
                monitor_child,
                Arc::new(AtomicBool::new(false)),
                2,
                &tx,
                &ctx,
            );
        });
        assert!(matches!(
            rx.recv_timeout(Duration::from_secs(3)).unwrap(),
            Event::Lost(2, _)
        ));
        assert!(child.lock().unwrap().try_wait().unwrap().is_some());
        monitor.join().unwrap();
    }
}
