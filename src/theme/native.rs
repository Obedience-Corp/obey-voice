#[cfg(target_os = "macos")]
pub(super) fn native_backdrop(cc: &eframe::CreationContext<'_>) {
    use objc2::MainThreadMarker;
    use objc2_app_kit::{
        NSAutoresizingMaskOptions, NSView, NSVisualEffectBlendingMode, NSVisualEffectMaterial,
        NSVisualEffectState, NSVisualEffectView, NSWindowOrderingMode,
    };
    use raw_window_handle::{HasWindowHandle, RawWindowHandle};
    let (Some(mtm), Ok(handle)) = (MainThreadMarker::new(), cc.window_handle()) else {
        return;
    };
    let RawWindowHandle::AppKit(handle) = handle.as_raw() else {
        return;
    };
    // SAFETY: eframe owns this NSView for the lifetime of CreationContext and
    // calls this on AppKit's main thread. The parent retains the inserted view.
    // Insert below the render view, keeping the backdrop out of the input hierarchy.
    unsafe {
        let view: &NSView = handle.ns_view.cast().as_ref();
        let Some(parent) = view.superview() else {
            return;
        };
        let effect = NSVisualEffectView::initWithFrame(mtm.alloc(), view.frame());
        effect.setMaterial(NSVisualEffectMaterial::Sidebar);
        effect.setBlendingMode(NSVisualEffectBlendingMode::BehindWindow);
        effect.setState(NSVisualEffectState::FollowsWindowActiveState);
        effect.setAutoresizingMask(
            NSAutoresizingMaskOptions::ViewWidthSizable
                | NSAutoresizingMaskOptions::ViewHeightSizable,
        );
        parent.addSubview_positioned_relativeTo(&effect, NSWindowOrderingMode::Below, Some(view));
    }
}

#[cfg(target_os = "macos")]
pub fn native_appearance(window: &impl raw_window_handle::HasWindowHandle, dark: bool) {
    use objc2_app_kit::{
        NSAppearance, NSAppearanceCustomization, NSAppearanceNameAqua, NSAppearanceNameDarkAqua,
        NSView,
    };
    use raw_window_handle::RawWindowHandle;
    let Ok(handle) = window.window_handle() else {
        return;
    };
    let RawWindowHandle::AppKit(handle) = handle.as_raw() else {
        return;
    };
    // SAFETY: called on eframe's main thread while the window is alive.
    unsafe {
        let view: &NSView = handle.ns_view.cast().as_ref();
        if let Some(window) = view.window() {
            let appearance = NSAppearance::appearanceNamed(if dark {
                NSAppearanceNameDarkAqua
            } else {
                NSAppearanceNameAqua
            });
            window.setAppearance(appearance.as_deref());
        }
    }
}
