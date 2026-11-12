package brain

import (
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// ReplyMediaWriter updates an already committed assistant reply on its owning
// goroutine. It preserves live provider sessions and immutable response identity.
type ReplyMediaWriter interface {
	SetReplyMedia(string, []chatmedia.Ref) error
}

func replyIndex(turns []Turn) int {
	for i := len(turns) - 1; i >= 0; i-- {
		if (turns[i].Role == "assistant" || turns[i].Role == "samantha") && len(turns[i].ToolCalls) == 0 {
			return i
		}
	}
	return -1
}
func setReplyMedia(turns []Turn, text string, refs []chatmedia.Ref) error {
	i := replyIndex(turns)
	if i < 0 {
		return errors.New("no assistant reply to attach images to")
	}
	turns[i].Content = text
	turns[i].Attachments = append([]chatmedia.Ref(nil), refs...)
	return nil
}

// SetRecordedReplyMedia moves images into the newest turn that is reply, by
// response id and text, so a reply recorded after it never takes them.
func SetRecordedReplyMedia(turns []Turn, reply Turn, text string, refs []chatmedia.Ref) error {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if (t.Role == "assistant" || t.Role == "samantha") && len(t.ToolCalls) == 0 && t.ResponseID == reply.ResponseID && t.Content == reply.Content {
			turns[i].Content = text
			turns[i].Attachments = append([]chatmedia.Ref(nil), refs...)
			return nil
		}
	}
	return errors.New("the reply to attach images to is no longer in the conversation")
}
func setMediaViews(canonical, working []Turn, text string, refs []chatmedia.Ref) error {
	i := replyIndex(canonical)
	if i < 0 {
		return errors.New("no assistant reply to attach images to")
	}
	// A bounded/compacted working view may already have discarded this reply.
	// Never attach its images to an earlier answer or a summary.
	if j := replyIndex(working); j >= 0 && working[j].Content == canonical[i].Content && working[j].ResponseID == canonical[i].ResponseID {
		_ = setReplyMedia(working, text, refs)
	}
	return setReplyMedia(canonical, text, refs)
}
func (o *OllamaBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	original := ""
	if i := replyIndex(o.canonical); i >= 0 {
		original = o.canonical[i].Content
	}
	if err := setMediaViews(o.canonical, o.working, text, refs); err != nil {
		return err
	}
	// Native history remains text at assistant authority. Images are rehydrated
	// by prepareImageHistory when another model request needs them.
	for i := len(o.history) - 1; i >= 0; i-- {
		if o.history[i].Role == "assistant" && len(o.history[i].ToolCalls) == 0 && o.history[i].Content == original {
			o.history[i].Content = text
			break
		}
	}
	return nil
}
func (c *CodexBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	return setMediaViews(c.canonical, c.working, text, refs)
}
func (b *ObeyBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return setMediaViews(b.canonical, b.working, text, refs)
}
func (b *MuseSparkBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return setMediaViews(b.canonical, b.working, text, refs)
}
func (b *Brain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return setReplyMedia(b.history, text, refs)
}
func (g *GrokBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return setReplyMedia(g.history, text, refs)
}
func (c *CursorBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return setReplyMedia(c.history, text, refs)
}

// ReplyWorkDir is the frozen provider's directory, not the UI's current choice.
func ReplyWorkDir(provider Provider) string {
	var cfg *config.Config
	switch b := provider.(type) {
	case *OllamaBrain:
		cfg = b.cfg
	case *CodexBrain:
		cfg = b.cfg
	case *ObeyBrain:
		cfg = b.cfg
	case *MuseSparkBrain:
		cfg = b.cfg
	case *Brain:
		cfg = b.cfg
	case *GrokBrain:
		cfg = b.cfg
	case *CursorBrain:
		cfg = b.cfg
	default:
		return ""
	}
	return config.WorkDirFrom(cfg)
}
