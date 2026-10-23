//! One menu policy for native adapters and execution-time validation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Action {
    Open,
    Conversation,
    Meeting,
    Microphone,
    Settings,
    Diagnostics,
    Quit,
}
impl Action {
    pub const ALL: [Self; 7] = [
        Self::Open,
        Self::Conversation,
        Self::Meeting,
        Self::Microphone,
        Self::Settings,
        Self::Diagnostics,
        Self::Quit,
    ];
    #[cfg(target_os = "macos")]
    pub fn id(self) -> String {
        format!("obey.{self:?}")
    }
}

#[derive(Clone, Default, Debug, PartialEq, Eq)]
pub struct Snapshot {
    pub connected: bool,
    pub setup: bool,
    pub transitioning: bool,
    pub conversation: bool,
    pub microphone: bool,
    pub voice_available: bool,
    pub meeting: bool,
    pub paused: bool,
    pub error: bool,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Activity {
    Idle,
    Listening,
    Recording,
    Paused,
    Error,
}
impl Snapshot {
    pub fn activity(&self) -> Activity {
        if !self.connected {
            return Activity::Error;
        }
        if self.meeting {
            return if self.paused {
                Activity::Paused
            } else {
                Activity::Recording
            };
        }
        if self.conversation && self.microphone {
            return Activity::Listening;
        }
        if self.error {
            return Activity::Error;
        }
        if self.conversation && self.voice_available {
            return Activity::Paused;
        }
        Activity::Idle
    }
    pub fn title(&self) -> String {
        let status = match self.activity() {
            Activity::Idle => "Ready",
            Activity::Listening => "Microphone on",
            Activity::Recording => "Recording meeting",
            Activity::Paused => "Microphone paused",
            Activity::Error if !self.connected => "Engine offline",
            Activity::Error => "Needs attention",
        };
        format!(
            "Obey — {status}{}",
            if self.error && self.activity() != Activity::Error {
                " · Error"
            } else {
                ""
            }
        )
    }
    pub fn label(&self, action: Action) -> &'static str {
        match action {
            Action::Open => "Open Obey",
            Action::Conversation => "New conversation…",
            Action::Meeting if self.meeting => "Stop and review meeting",
            Action::Meeting => "Start meeting…",
            Action::Microphone if self.meeting && self.paused => "Resume meeting",
            Action::Microphone if self.meeting => "Pause meeting",
            Action::Microphone if self.microphone => "Pause microphone",
            Action::Microphone => "Resume microphone",
            Action::Settings => "Settings…",
            Action::Diagnostics => "Diagnostics…",
            Action::Quit => "Quit Obey",
        }
    }
    pub fn enabled(&self, action: Action) -> bool {
        match action {
            Action::Open | Action::Diagnostics | Action::Quit => true,
            Action::Settings => !self.setup && !self.transitioning && !self.meeting,
            Action::Conversation => {
                self.connected
                    && !self.setup
                    && !self.transitioning
                    && !self.meeting
                    && !self.conversation
            }
            Action::Meeting => {
                self.connected && !self.setup && !self.transitioning && !self.conversation
            }
            Action::Microphone => {
                self.connected
                    && !self.setup
                    && !self.transitioning
                    && (self.meeting || (self.conversation && self.voice_available))
            }
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn capture_remains_visible_when_an_unrelated_error_occurs() {
        let mut s = Snapshot {
            connected: true,
            meeting: true,
            error: true,
            ..Default::default()
        };
        assert_eq!(s.activity(), Activity::Recording);
        assert!(s.title().contains("Error"));
        s.paused = true;
        assert_eq!(s.activity(), Activity::Paused);
        s.connected = false;
        assert_eq!(s.activity(), Activity::Error);
    }
    #[test]
    fn conflicting_and_stale_actions_are_disabled() {
        let mut s = Snapshot {
            connected: true,
            ..Default::default()
        };
        assert!(s.enabled(Action::Conversation));
        assert!(!s.enabled(Action::Microphone));
        s.meeting = true;
        assert!(!s.enabled(Action::Conversation));
        assert!(!s.enabled(Action::Settings));
        assert!(s.enabled(Action::Meeting));
        assert!(s.enabled(Action::Microphone));
        s.transitioning = true;
        assert!(!s.enabled(Action::Meeting));
        assert!(!s.enabled(Action::Microphone));
        s.setup = true;
        assert!(s.enabled(Action::Quit));
        assert!(s.enabled(Action::Diagnostics));
    }
}
