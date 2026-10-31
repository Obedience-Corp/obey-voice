use serde_json::Value;

#[derive(Clone, Debug, Default, PartialEq)]
pub struct AudioDraft {
    pub present: bool,
    pub mode: String,
    pub provider: String,
    pub model: String,
    pub endpoint: String,
    pub voice: String,
    pub controls: String,
    pub transcript: String,
    pub retain: bool,
}

impl AudioDraft {
    pub fn load(value: &Value) -> Self {
        let string = |key: &str| value[key].as_str().unwrap_or_default().to_owned();
        Self {
            present: value.is_object(),
            mode: string("mode"),
            provider: string("provider"),
            model: string("model"),
            endpoint: string("endpoint"),
            voice: string("voice"),
            controls: string("controls"),
            transcript: string("transcript"),
            retain: value["retain_audio"].as_bool().unwrap_or(false),
        }
    }

    pub fn native(&self) -> bool {
        self.mode == "native"
    }

    pub fn append_flags(&self, args: &mut Vec<String>) {
        if !self.present && !self.native() {
            return;
        }
        for (flag, value) in [
            ("audio-mode", self.mode.as_str()),
            ("audio-provider", self.provider.as_str()),
            ("audio-model", self.model.as_str()),
            ("audio-endpoint", self.endpoint.as_str()),
            ("audio-voice", self.voice.as_str()),
            ("audio-controls", self.controls.as_str()),
            ("audio-transcript", self.transcript.as_str()),
        ] {
            args.extend([format!("--{flag}"), value.into()]);
        }
        args.push(format!("--retain-audio={}", self.retain));
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn ordinary_profile_keeps_audio_omitted_and_switch_keeps_native_choices() {
        let mut args = vec![];
        AudioDraft::load(&Value::Null).append_flags(&mut args);
        assert!(args.is_empty());
        let mut draft = AudioDraft::load(
            &json!({"mode":"native","voice":"female","transcript":"off","controls":"conversation","retain_audio":true}),
        );
        draft.mode = "standard".into();
        draft.append_flags(&mut args);
        assert!(
            args.windows(2)
                .any(|pair| pair == ["--audio-voice", "female"])
        );
        assert!(
            args.windows(2)
                .any(|pair| pair == ["--audio-mode", "standard"])
        );
        assert!(args.contains(&"--retain-audio=true".into()));
    }
}
