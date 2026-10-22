//! StatusNotifierItem with a live host check: a watcher alone is not a panel.
use super::{Action, Dispatch, Snapshot, artwork};
use dbus::blocking::{Connection, stdintf::org_freedesktop_dbus::Properties};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
    mpsc,
};
use std::time::Duration;

pub struct Tray {
    handle: ksni::Handle<Item>,
    shown: Snapshot,
    dispatch: Dispatch,
    stop: mpsc::Sender<()>,
}
impl Tray {
    pub fn new(dispatch: Dispatch, state: Snapshot) -> Result<Self, String> {
        let registered = Arc::new(AtomicBool::new(false));
        let item = Item {
            state: state.clone(),
            dispatch: dispatch.clone(),
            registered: registered.clone(),
        };
        let service = ksni::TrayService::new(item);
        let handle = service.handle();
        let life = dispatch.clone();
        let alive = Arc::new(AtomicBool::new(true));
        let service_alive = alive.clone();
        std::thread::spawn(move || {
            if let Err(error) = service.run() {
                crate::diagnostics::record(
                    "error",
                    "tray.service",
                    serde_json::json!({"error":error.to_string()}),
                );
            }
            service_alive.store(false, Ordering::Release);
            life.available(false);
        });
        let (stop, stopped) = mpsc::channel();
        let host = dispatch.clone();
        std::thread::spawn(move || {
            let connection = Connection::new_session().ok();
            loop {
                let available = registered.load(Ordering::Acquire)
                    && alive.load(Ordering::Acquire)
                    && connection.as_ref().is_some_and(host_registered);
                host.available(available);
                if stopped.recv_timeout(Duration::from_secs(1))
                    != Err(mpsc::RecvTimeoutError::Timeout)
                {
                    break;
                }
            }
            host.available(false);
        });
        Ok(Self {
            handle,
            shown: state,
            dispatch,
            stop,
        })
    }
    pub fn update(&mut self, state: &Snapshot) {
        if self.shown != *state {
            crate::diagnostics::record(
                "trace",
                "tray.state",
                serde_json::json!({"title":state.title()}),
            );
            self.handle.update(|item| item.state = state.clone());
            self.shown = state.clone();
        }
    }
}
impl Drop for Tray {
    fn drop(&mut self) {
        let _ = self.stop.send(());
        self.handle.shutdown();
        self.dispatch.available(false);
    }
}
fn host_registered(connection: &Connection) -> bool {
    connection
        .with_proxy(
            "org.kde.StatusNotifierWatcher",
            "/StatusNotifierWatcher",
            Duration::from_millis(500),
        )
        .get::<bool>(
            "org.kde.StatusNotifierWatcher",
            "IsStatusNotifierHostRegistered",
        )
        .unwrap_or(false)
}
struct Item {
    state: Snapshot,
    dispatch: Dispatch,
    registered: Arc<AtomicBool>,
}
impl ksni::Tray for Item {
    fn id(&self) -> String {
        "obey-voice-agent".into()
    }
    fn title(&self) -> String {
        self.state.title()
    }
    fn tool_tip(&self) -> ksni::ToolTip {
        ksni::ToolTip {
            title: self.state.title(),
            description: "Click to open Obey Voice".into(),
            ..Default::default()
        }
    }
    fn icon_pixmap(&self) -> Vec<ksni::Icon> {
        let rgba = artwork::rgba(self.state.activity(), false);
        let data = rgba
            .chunks_exact(4)
            .flat_map(|p| [p[3], p[0], p[1], p[2]])
            .collect();
        vec![ksni::Icon {
            width: 32,
            height: 32,
            data,
        }]
    }
    fn activate(&mut self, _: i32, _: i32) {
        self.dispatch.send(Action::Open);
    }
    fn menu(&self) -> Vec<ksni::MenuItem<Self>> {
        let mut menu = vec![
            ksni::menu::StandardItem {
                label: self.state.title(),
                enabled: false,
                ..Default::default()
            }
            .into(),
        ];
        menu.extend(Action::ALL.into_iter().map(|action| {
            ksni::menu::StandardItem {
                label: self.state.label(action).into(),
                enabled: self.state.enabled(action),
                activate: Box::new(move |item: &mut Self| item.dispatch.send(action)),
                ..Default::default()
            }
            .into()
        }));
        menu
    }
    fn watcher_online(&self) {
        self.registered.store(true, Ordering::Release);
    }
    fn watcher_offine(&self) -> bool {
        self.registered.store(false, Ordering::Release);
        self.dispatch.available(false);
        true
    }
}

#[cfg(test)]
#[path = "linux_tests.rs"]
mod tests;
