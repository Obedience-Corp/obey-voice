//! Restore the existing window when the app is reopened from Finder or the Dock.
//! Keep winit's delegate identity and never replace an existing delegate method.
use super::{Action, Dispatch};
use objc2::runtime::{AnyClass, AnyObject, Bool, Imp, Sel};
use objc2::{Encode, MainThreadMarker, sel};
use objc2_app_kit::NSApplication;
use std::ffi::CString;
use std::sync::Mutex;

static DISPATCH: Mutex<Option<Dispatch>> = Mutex::new(None);
type Reopen = unsafe extern "C-unwind" fn(&AnyObject, Sel, *mut AnyObject, Bool) -> Bool;

unsafe extern "C-unwind" fn reopen(_: &AnyObject, _: Sel, _: *mut AnyObject, _: Bool) -> Bool {
    if let Some(dispatch) = &*DISPATCH.lock().unwrap_or_else(|e| e.into_inner()) {
        dispatch.send(Action::Open);
    }
    Bool::YES
}

pub(super) fn install(dispatch: Dispatch) -> Result<(), String> {
    let main = MainThreadMarker::new().ok_or("reopen hook requires main thread")?;
    let delegate = NSApplication::sharedApplication(main)
        .delegate()
        .ok_or("application delegate is not installed")?;
    let object: &AnyObject = (*delegate).as_ref();
    install_on_class(object.class(), dispatch)
}

fn install_on_class(class: &'static AnyClass, dispatch: Dispatch) -> Result<(), String> {
    let selector = sel!(applicationShouldHandleReopen:hasVisibleWindows:);
    // IMP is Objective-C's erased method pointer. Encoding below matches this
    // exact callback signature (BOOL return and id, SEL, id, BOOL arguments).
    let callback = unsafe { std::mem::transmute::<Reopen, Imp>(reopen) };
    if let Some(method) = class.instance_method(selector) {
        if !std::ptr::fn_addr_eq(method.implementation(), callback) {
            return Err("application delegate already handles reopen".into());
        }
    } else {
        let encoding = CString::new(format!("{}@:@{}", Bool::ENCODING, Bool::ENCODING)).unwrap();
        let added = unsafe {
            objc2::ffi::class_addMethod(
                class as *const AnyClass as *mut AnyClass,
                selector,
                callback,
                encoding.as_ptr(),
            )
        };
        if !added.as_bool() {
            return Err("could not install reopen callback".into());
        }
    }
    *DISPATCH.lock().unwrap_or_else(|e| e.into_inner()) = Some(dispatch);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use objc2::{msg_send, rc::Retained, runtime::ClassBuilder};
    use std::sync::{Arc, mpsc};

    #[test]
    fn reopen_preserves_delegate_identity_and_wakes_the_existing_app() {
        let class = ClassBuilder::new(
            c"ObeyReopenCallbackTest",
            AnyClass::get(c"NSObject").unwrap(),
        )
        .unwrap()
        .register();
        let (tx, events) = mpsc::channel();
        let dispatch = Dispatch {
            tx,
            ctx: Default::default(),
            available: Arc::default(),
        };
        install_on_class(class, dispatch.clone()).unwrap();
        install_on_class(class, dispatch).unwrap();
        let receiver: Retained<AnyObject> = unsafe { msg_send![class, new] };
        let reply: Bool = unsafe {
            msg_send![&*receiver,
            applicationShouldHandleReopen: std::ptr::null_mut::<AnyObject>(),
            hasVisibleWindows: Bool::NO]
        };
        assert!(reply.as_bool());
        assert_eq!(events.try_recv().unwrap(), Action::Open);
        assert_eq!(receiver.class(), class);
    }

    unsafe extern "C-unwind" fn existing(
        _: *mut AnyObject,
        _: Sel,
        _: *mut AnyObject,
        _: Bool,
    ) -> Bool {
        Bool::NO
    }

    #[test]
    fn existing_reopen_policy_is_not_overwritten() {
        let mut builder = ClassBuilder::new(
            c"ObeyExistingReopenTest",
            AnyClass::get(c"NSObject").unwrap(),
        )
        .unwrap();
        unsafe {
            builder.add_method(
                sel!(applicationShouldHandleReopen:hasVisibleWindows:),
                existing as unsafe extern "C-unwind" fn(_, _, _, _) -> _,
            );
        }
        let class = builder.register();
        let before = class
            .instance_method(sel!(applicationShouldHandleReopen:hasVisibleWindows:))
            .unwrap()
            .implementation();
        let (tx, _) = mpsc::channel();
        let dispatch = Dispatch {
            tx,
            ctx: Default::default(),
            available: Arc::default(),
        };
        assert!(install_on_class(class, dispatch).is_err());
        let after = class
            .instance_method(sel!(applicationShouldHandleReopen:hasVisibleWindows:))
            .unwrap()
            .implementation();
        assert!(std::ptr::fn_addr_eq(before, after));
    }
}
