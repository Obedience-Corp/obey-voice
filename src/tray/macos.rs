//! Native AppKit status item, using the same tray-icon version as Keepshot.
use super::{Action, Dispatch, Snapshot, artwork, status::Activity};
use tray_icon::menu::{Menu, MenuEvent, MenuItem};
use tray_icon::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent};

pub(super) fn install(dispatch: Dispatch) {
    if let Err(error) = super::macos_reopen::install(dispatch.clone()) {
        crate::diagnostics::record(
            "error",
            "desktop.reopen",
            serde_json::json!({"error":error}),
        );
    }
    let clicks = dispatch.clone();
    TrayIconEvent::set_event_handler(Some(move |event| {
        if let TrayIconEvent::Click {
            button: MouseButton::Left,
            button_state: MouseButtonState::Up,
            ..
        } = event
        {
            clicks.send(Action::Open);
        }
    }));
    MenuEvent::set_event_handler(Some(move |event: MenuEvent| {
        if let Some(action) = Action::ALL
            .into_iter()
            .find(|a| event.id.as_ref() == a.id())
        {
            dispatch.send(action);
        }
    }));
}

pub struct Tray {
    icon: TrayIcon,
    status: MenuItem,
    items: Vec<(Action, MenuItem)>,
    shown: Snapshot,
    dispatch: Dispatch,
}
impl Tray {
    pub fn new(dispatch: Dispatch, state: Snapshot) -> Result<Self, String> {
        let menu = Menu::new();
        let status = MenuItem::new(state.title(), false, None);
        menu.append(&status).map_err(|e| e.to_string())?;
        let mut items = Vec::new();
        for action in Action::ALL {
            let item = MenuItem::with_id(
                action.id(),
                state.label(action),
                state.enabled(action),
                None,
            );
            menu.append(&item).map_err(|e| e.to_string())?;
            items.push((action, item));
        }
        let idle = state.activity() == Activity::Idle;
        let icon = TrayIconBuilder::new()
            .with_tooltip(state.title())
            .with_icon(make_icon(&state)?)
            .with_icon_as_template(idle)
            .with_menu(Box::new(menu))
            .with_menu_on_left_click(false)
            .with_menu_on_right_click(true)
            .build()
            .map_err(|e| e.to_string())?;
        crate::diagnostics::record(
            "info",
            "tray.created",
            serde_json::json!({"title":state.title()}),
        );
        dispatch.available(true);
        Ok(Self {
            icon,
            status,
            items,
            shown: state,
            dispatch,
        })
    }
    pub fn update(&mut self, state: &Snapshot) {
        if self.shown == *state {
            return;
        }
        crate::diagnostics::record(
            "trace",
            "tray.state",
            serde_json::json!({"title":state.title()}),
        );
        let result = make_icon(state).and_then(|icon| {
            self.icon
                .set_icon_with_as_template(Some(icon), state.activity() == Activity::Idle)
                .map_err(|e| e.to_string())
        });
        if let Err(error) = result {
            crate::diagnostics::record("error", "tray.icon", serde_json::json!({"error":error}));
        }
        let _ = self.icon.set_tooltip(Some(state.title()));
        self.status.set_text(state.title());
        for (action, item) in &self.items {
            item.set_text(state.label(*action));
            item.set_enabled(state.enabled(*action));
        }
        self.shown = state.clone();
    }
}
impl Drop for Tray {
    fn drop(&mut self) {
        let _ = self.icon.set_visible(false);
        self.dispatch.available(false);
    }
}
fn make_icon(state: &Snapshot) -> Result<tray_icon::Icon, String> {
    // Active icons use colored Corp artwork so they retain contrast without template tinting.
    tray_icon::Icon::from_rgba(
        artwork::rgba(state.activity(), state.activity() == Activity::Idle),
        32,
        32,
    )
    .map_err(|e| e.to_string())
}
