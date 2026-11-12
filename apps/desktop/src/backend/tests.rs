use super::protocol::read_protocol;
use super::*;
use std::process::{Command, Stdio};

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
    // Immutable checked-in executable: publishing a fresh writable script
    // immediately before parallel exec can transiently fail with ETXTBSY.
    let script = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/noisy-engine.sh");
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

// A model move cleans up after SIGTERM. Cancel used to SIGKILL it five
// seconds later, cutting that cleanup short.
#[cfg(unix)]
#[test]
fn a_cancelled_model_move_is_signalled_once_and_never_killed() {
    let script =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/slow-move-engine.sh");
    let log = std::env::temp_dir().join(format!("obey-move-cancel-{}.log", std::process::id()));
    let _ = std::fs::remove_file(&log);
    let mut backend = Backend::new(eframe::egui::Context::default(), true);
    backend.engine = script;
    let args = ["models", "move", "--"]
        .map(String::from)
        .into_iter()
        .chain([log.display().to_string()])
        .collect();
    backend.cli(7, args, true);
    // Cancel only once the stand-in is ready to count signals.
    loop {
        match backend
            .events
            .recv_timeout(Duration::from_secs(10))
            .unwrap()
        {
            Event::Log { id: 7, line } if line == "ready" => break,
            Event::Result { id: 7, result } => panic!("the stand-in ended early: {result:?}"),
            _ => {}
        }
    }
    backend.cancel(7);
    thread::sleep(Duration::from_millis(300));
    backend.cancel(7);
    assert!(backend.cleaning_up(7));
    loop {
        if let Event::Result { id: 7, result } = backend
            .events
            .recv_timeout(Duration::from_secs(20))
            .unwrap()
        {
            assert!(result.is_ok(), "the move was killed: {result:?}");
            break;
        }
    }
    let terms = std::fs::read_to_string(&log).unwrap_or_default();
    let _ = std::fs::remove_file(&log);
    assert_eq!(terms.lines().count(), 1, "signals received: {terms:?}");
}
