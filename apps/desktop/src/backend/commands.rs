use super::process::{finish_child, signal_child, spawn_command};
use super::protocol::send;
use super::{Backend, Event};
use serde_json::{Value, json};
use std::io::BufReader;
use std::io::{BufRead, Read};
use std::path::PathBuf;
use std::sync::atomic::Ordering;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

impl Backend {
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
        let gentle_jobs = self.gentle.clone();
        let signalled = self.signalled.clone();
        let gentle = super::process::finishes_on_its_own(&args);
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
                    if gentle {
                        gentle_jobs.lock().unwrap().insert(id);
                    }
                    if stopped.load(Ordering::Acquire) || cancelled.lock().unwrap().remove(&id) {
                        signal_child(&mut child.lock().unwrap());
                        if gentle {
                            signalled.lock().unwrap().insert(id);
                        } else {
                            finish_child(child.clone(), Duration::from_secs(2));
                        }
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
                gentle_jobs.lock().unwrap().remove(&id);
                signalled.lock().unwrap().remove(&id);
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
}
