package utterance

// DisplayText is presentation only. Model requests use actual typed text,
// verified audio or a ready transcript, never this audio-message label.
func DisplayText(text string, input *Finalized) string {
	if text != "" || input == nil {
		return text
	}
	switch input.Transcript.State {
	case Ready:
		return input.Transcript.Text
	case Disabled:
		return "Audio message (transcript off)"
	case Pending:
		return "Audio message (transcript pending)"
	default:
		return "Audio message (transcript unavailable)"
	}
}

// DisplayAudioInput preserves an actual typed instruction beside its recording
// caption. Neither the labels nor this presentation are sent to the model.
func DisplayAudioInput(typed string, input *Finalized) string {
	if typed == "" {
		return DisplayText("", input)
	}
	if input == nil {
		return typed
	}
	if input.Transcript.State == Ready {
		return typed + "\nTranscript: " + input.Transcript.Text
	}
	return typed + "\n" + DisplayText("", input)
}
