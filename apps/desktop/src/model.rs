use serde::Deserialize;
use serde_json::Value;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Screen {
    Setup,
    Home,
    Chat,
    Sessions,
    Personas,
    Workers,
    Meeting,
    Library,
    Audiobook,
    Remote,
    Settings,
}
impl Screen {
    pub const ALL: [Self; 10] = [
        Self::Home,
        Self::Chat,
        Self::Sessions,
        Self::Personas,
        Self::Workers,
        Self::Meeting,
        Self::Library,
        Self::Audiobook,
        Self::Remote,
        Self::Settings,
    ];
    pub fn title(self) -> &'static str {
        match self {
            Self::Setup => "Welcome to Obey",
            Self::Home => "Home",
            Self::Chat => "Conversation",
            Self::Sessions => "Sessions",
            Self::Personas => "Personas",
            Self::Workers => "Workers",
            Self::Meeting => "Meeting",
            Self::Library => "Library",
            Self::Audiobook => "Create audiobook",
            Self::Remote => "Use on another device",
            Self::Settings => "Settings",
        }
    }
    pub fn manager(self) -> bool {
        matches!(self, Self::Personas | Self::Workers | Self::Settings)
    }
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Turn {
    #[serde(deserialize_with = "nullable_vec")]
    pub attachments: Vec<crate::app::media::Attachment>,
    pub role: String,
    pub content: String,
    pub name: String,
    pub thinking: String,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Activity {
    pub stage: String,
    pub detail: String,
    pub milliseconds: i64,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Enrollment {
    pub active: bool,
    pub name: String,
    pub clip: u32,
    pub seconds: f32,
    pub processing: bool,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct State {
    #[serde(deserialize_with = "nullable_vec")]
    pub attachments: Vec<crate::app::media::Attachment>,
    pub attachment_busy: bool,
    pub attachment_error: String,
    pub participation: Option<ParticipationSettings>,
    pub work_dir: String,
    pub work_dir_source: String,
    #[serde(deserialize_with = "nullable_vec")]
    pub notices: Vec<String>,
    #[serde(deserialize_with = "nullable_vec")]
    pub work_results: Vec<String>,
    pub worker_threads: std::collections::HashMap<String, Vec<Turn>>,
    #[serde(default)]
    pub work_result_images: Vec<Vec<crate::app::media::Attachment>>,
    pub settings_message: String,
    pub enrollment: Enrollment,
    #[serde(deserialize_with = "nullable_vec")]
    pub speaker_names: Vec<String>,
    pub session_id: String,
    pub name: String,
    pub brain_provider: String,
    pub brain_model: String,
    pub status: String,
    pub error: bool,
    pub mic: bool,
    pub voice_available: bool,
    pub muted: bool,
    pub busy: bool,
    pub recording: String,
    pub thinking: String,
    pub response: String,
    pub input_level: f32,
    pub output_level: f32,
    #[serde(deserialize_with = "nullable_vec")]
    pub turns: Vec<Turn>,
    #[serde(deserialize_with = "nullable_vec")]
    pub activity: Vec<Activity>,
    #[serde(deserialize_with = "nullable_vec")]
    pub workers: Vec<Value>,
    pub workers_enabled: bool,
}
impl State {
    pub fn brain_label(&self) -> Option<String> {
        let provider = self.brain_provider.trim();
        if provider.is_empty() {
            return None;
        }
        let model = self.brain_model.trim();
        Some(format!(
            "Brain: {provider} · {}",
            if model.is_empty() { "default" } else { model }
        ))
    }
}
fn nullable_vec<'de, D, T>(deserializer: D) -> Result<Vec<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    Ok(Option::<Vec<T>>::deserialize(deserializer)?.unwrap_or_default())
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Session {
    pub id: String,
    pub summary: String,
    pub updated_at: String,
    pub provider: String,
    pub model: String,
    pub turns: Option<Vec<Value>>,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Persona {
    pub id: String,
    pub display_name: String,
    pub provider: String,
    pub voice: String,
    pub active: bool,
    pub inactive: bool,
    pub builtin: bool,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct KeySpec {
    pub key: String,
    pub title: String,
    pub help: String,
    pub group: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(rename = "enum")]
    pub choices: Vec<String>,
    pub editable: bool,
    pub secret: bool,
    pub restart_required: bool,
    pub managed_by: String,
    pub default: Value,
    pub allows_empty: bool,
}
#[derive(Default)]
pub struct PersonaForm {
    pub preview_persona: String,
    pub preview_request: Option<u64>,
    pub preview_notice: String,
    pub preview_error: bool,
    pub save_error: String,
    pub starting_directory: String,
    pub original_directory: String,
    pub directory_choices: Vec<Value>,
    pub directory_notice: String,
    pub directory_request: Option<u64>,
    pub browse_request: Option<u64>,
    pub id: String,
    pub name: String,
    pub prompt: String,
    pub brain: String,
    pub model: String,
    pub tts: String,
    pub voice: String,
    pub tier: String,
    pub structured: bool,
    pub open: bool,
}
#[derive(Clone)]
pub enum Purpose {
    Setup(crate::setup::Operation),
    Sessions,
    Personas,
    PersonaLoad,
    PersonaPreview,
    PersonaDirectories,
    PersonaBrowse,
    SettingsSchema,
    WorkerConfig,
    SettingsValues,
    SettingSave(String),
    Mutation(String),
    Library,
    Book,
    Meetings,
    Campaigns,
    MeetingRoute,
    MeetingNote(String),
    MeetingPaused(bool),
    MeetingDocument,
    Task,
    Start,
    Stop,
    MeetingStart,
    MeetingStop,
    Ignore,
}
pub fn string(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}
pub fn display(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        Value::Null => String::new(),
        _ => v.to_string(),
    }
}

/// Routing can fail with process exit status zero; the receipt is authoritative.
pub fn route_receipt(value: &Value) -> Result<String, String> {
    let detail = string(value, "detail");
    let error = string(value, "error");
    match value["outcome"].as_str() {
        Some("routed" | "delegated") if error.is_empty() => Ok(format!(
            "Notes filed to {}: {}",
            string(value, "destination_id"),
            detail
        )),
        Some("failed") => Err(format!(
            "Meeting routing failed: {}",
            if error.is_empty() { &detail } else { &error }
        )),
        Some("skipped") => Ok("Meeting routing skipped; notes remain local".into()),
        _ => Err(format!("Meeting routing did not confirm delivery: {value}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn active_brain_label_uses_snapshot_and_handles_older_engines() {
        let s: State = serde_json::from_str(
            r#"{"name":"Jarvis","brain_provider":"cursor","brain_model":"composer-2.5"}"#,
        )
        .unwrap();
        assert_eq!(
            s.brain_label().as_deref(),
            Some("Brain: cursor · composer-2.5")
        );
        let s: State = serde_json::from_str(r#"{"brain_provider":"claude"}"#).unwrap();
        assert_eq!(s.brain_label().as_deref(), Some("Brain: claude · default"));
        let legacy: State = serde_json::from_str(r#"{"name":"Obey"}"#).unwrap();
        assert_eq!(legacy.brain_label(), None);
    }
    #[test]
    fn null_upstream_lists_are_empty() {
        let s: State =
            serde_json::from_str(r#"{"turns":null,"workers":null,"activity":null,"name":"Obey"}"#)
                .unwrap();
        assert!(s.turns.is_empty());
        assert_eq!(s.name, "Obey");
    }
    #[test]
    fn preserves_reply_identity() {
        let s: State = serde_json::from_str(
            r#"{"name":"Next","turns":[{"role":"assistant","name":"Previous","content":"Hello"}]}"#,
        )
        .unwrap();
        assert_eq!(s.turns[0].name, "Previous");
    }
    #[test]
    fn routing_receipt_detects_failure_even_after_successful_process_exit() {
        assert!(
            route_receipt(&serde_json::json!({"outcome":"failed","error":"camp not found"}))
                .unwrap_err()
                .contains("camp not found")
        );
        assert!(route_receipt(&serde_json::json!({"outcome":"routed","destination_id":"camp:test","detail":"notes/meetings/test.md"})).unwrap().contains("notes/meetings/test.md"));
        assert!(route_receipt(&serde_json::json!({})).is_err());
    }
}

#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct ParticipationSettings {
    pub mode: String,
    pub members: Option<Vec<String>>,
}
