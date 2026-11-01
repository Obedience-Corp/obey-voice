package pipeline

import (
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

var ErrAudioMediaOwner = errors.New("recording belongs to another conversation media owner; start a new context explicitly")

// Media remains conversation-owned across native/Standard persona changes.
// Remembering the owner prevents a new native binding from silently losing
// canonical recording context by selecting a different in-memory store.
func (p *Pipeline) rememberAudioMedia(media *chatmedia.AudioSession) error {
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	if p.audioMedia != nil && p.audioMedia != media {
		return ErrAudioMediaOwner
	}
	p.audioMedia = media
	return nil
}

// Called after caption/capture joins and canonical history clear at the host's
// serialized lifecycle boundary. It releases turn references, not the borrowed
// media owner itself, so retained provider/caption holds remain authoritative.
func (p *Pipeline) releaseConversationAudio(turns []brain.Turn) {
	p.captureMu.Lock()
	owner := p.audioMedia
	if owner == nil && p.Native != nil {
		owner = p.Native.Media
	}
	p.audioMedia = nil
	p.captureMu.Unlock()
	if owner == nil {
		return
	}
	for _, turn := range turns {
		if turn.Utterance != nil && turn.Utterance.Audio != nil {
			owner.Release(*turn.Utterance.Audio)
		}
	}
}
