use super::protocol::send;
use super::{Backend, Event};
use serde_json::Value;
use std::process::Command;

impl Backend {
    pub fn pick_directory(&self, id: u64) {
        let tx = self.tx.clone();
        let ctx = self.ctx.clone();
        std::thread::spawn(move || {
            let result = pick_directory();
            send(&tx, &ctx, Event::Result { id, result });
        });
    }
}

fn pick_directory() -> Result<Value, String> {
    #[cfg(target_os = "macos")]
    let output = Command::new("/usr/bin/osascript")
        .args([
            "-e",
            "POSIX path of (choose folder with prompt \"Choose persona starting directory\")",
        ])
        .output();
    #[cfg(not(target_os = "macos"))]
    let output = Command::new("zenity")
        .args([
            "--file-selection",
            "--directory",
            "--title=Choose persona starting directory",
        ])
        .output();
    let output = output.map_err(|err| format!("Folder picker unavailable: {err}"))?;
    if !output.status.success() {
        let error = String::from_utf8_lossy(&output.stderr);
        if error.contains("(-128)") || (output.status.code() == Some(1) && error.trim().is_empty())
        {
            return Ok(Value::Null);
        }
        return Err(format!("Folder picker failed: {}", error.trim()));
    }
    let path = String::from_utf8(output.stdout).map_err(|err| err.to_string())?;
    Ok(Value::String(
        path.trim_end_matches(['\r', '\n']).to_owned(),
    ))
}
