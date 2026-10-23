//! Desktop presence. Platform callbacks only queue actions and wake app logic.
mod artwork;
#[cfg(target_os = "linux")]
mod linux;
#[cfg(target_os = "macos")]
mod macos;
#[cfg(target_os = "macos")]
mod macos_reopen;
#[cfg(target_os = "linux")]
use linux::Tray;
#[cfg(target_os = "macos")]
use macos::Tray;
mod status;
pub use status::{Action, Snapshot};

use eframe::egui;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
    mpsc,
};

#[derive(Clone)]
pub(super) struct Dispatch {
    tx: mpsc::Sender<Action>,
    ctx: egui::Context,
    available: Arc<AtomicBool>,
}
impl Dispatch {
    fn send(&self, action: Action) {
        let _ = self.tx.send(action);
        self.ctx.request_repaint();
    }
    fn available(&self, available: bool) {
        if self.available.swap(available, Ordering::AcqRel) != available {
            crate::diagnostics::record(
                "info",
                "tray.availability",
                serde_json::json!({"available":available}),
            );
            self.ctx.request_repaint();
        }
    }
}

pub struct Resident {
    pub enabled: bool,
    pub hidden: bool,
    pub quitting: bool,
    pub failure: Option<String>,
    dispatch: Dispatch,
    events: mpsc::Receiver<Action>,
    tray: Option<Tray>,
    attempted: bool,
}
impl Resident {
    pub fn new(ctx: egui::Context, enabled: bool) -> Self {
        let (tx, events) = mpsc::channel();
        let dispatch = Dispatch {
            tx,
            ctx,
            available: Arc::default(),
        };
        #[cfg(target_os = "macos")]
        macos::install(dispatch.clone());
        Self {
            enabled,
            hidden: false,
            quitting: false,
            failure: None,
            dispatch,
            events,
            tray: None,
            attempted: false,
        }
    }
    pub fn available(&self) -> bool {
        self.enabled && self.dispatch.available.load(Ordering::Acquire)
    }
    pub fn receive(&self) -> Option<Action> {
        self.events.try_recv().ok()
    }
    pub fn sync(&mut self, state: &Snapshot) {
        if !self.enabled {
            self.tray.take();
            self.dispatch.available(false);
            self.attempted = false;
            return;
        }
        if !self.attempted {
            self.attempted = true;
            // A retiring Linux service must not change the replacement service’s availability.
            self.dispatch.available = Arc::default();
            match Tray::new(self.dispatch.clone(), state.clone()) {
                Ok(tray) => {
                    self.tray = Some(tray);
                    self.failure = None;
                }
                Err(error) => {
                    crate::diagnostics::record(
                        "error",
                        "tray.create",
                        serde_json::json!({"error":error}),
                    );
                    self.failure = Some(error);
                }
            }
        }
        if let Some(tray) = &mut self.tray {
            tray.update(state);
        }
    }
}

pub fn hide_on_close(enabled: bool, available: bool, quitting: bool) -> bool {
    enabled && available && !quitting
}

#[cfg(not(any(target_os = "macos", target_os = "linux")))]
struct Tray;
#[cfg(not(any(target_os = "macos", target_os = "linux")))]
impl Tray {
    fn new(_: Dispatch, _: Snapshot) -> Result<Self, String> {
        Err("Tray unavailable on this platform".into())
    }
    fn update(&mut self, _: &Snapshot) {}
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn closing_never_hides_without_an_accessible_tray_or_when_quitting() {
        assert!(hide_on_close(true, true, false));
        for (enabled, available, quitting) in [
            (false, true, false),
            (true, false, false),
            (true, true, true),
        ] {
            assert!(!hide_on_close(enabled, available, quitting));
        }
    }
}
