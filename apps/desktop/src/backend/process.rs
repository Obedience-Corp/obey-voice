use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

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

pub(super) fn spawn_command(path: &PathBuf, args: &[String]) -> std::io::Result<Child> {
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
pub(super) fn signal_child(child: &mut Child) {
    #[cfg(unix)]
    // Each owned command has a new process group. Only signal that group.
    unsafe {
        libc::kill(-(child.id() as i32), libc::SIGTERM);
    }
    #[cfg(not(unix))]
    let _ = child.kill();
}
pub(super) fn finish_child(child: Arc<Mutex<Child>>, grace: Duration) {
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

/// A model move cleans up after SIGTERM, and a kill mid-cleanup leaves a
/// partial copy or a half-deleted old folder; such a command is signalled once
/// and never killed.
pub(crate) fn finishes_on_its_own(args: &[String]) -> bool {
    args.len() >= 2
        && args[0] == "models"
        && args[1] == "move"
        && !args.iter().any(|a| a == "--dry-run")
}
