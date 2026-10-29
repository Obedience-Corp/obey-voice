package events

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"

type DiscussionUpdated struct {
	SessionID         string `json:"session_id"`
	ConversationEpoch string `json:"conversation_epoch"`
	discussion.Snapshot
}

func (DiscussionUpdated) eventType() string { return "discussion_updated" }
