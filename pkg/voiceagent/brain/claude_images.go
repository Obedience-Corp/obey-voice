package brain

import (
	"context"
	"errors"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

// ValidateImages checks input images before a turn starts, with the same
// limits as Codex. It reads only the local attachment store.
func (b *Brain) ValidateImages(ctx context.Context, refs []chatmedia.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := loadImageData(refs)
	return err
}

// requestImages returns the blocks that follow a request's prompt: a label and
// an image for each image it carries, or none. A resumed session already holds
// earlier images and gets only attachments; a fresh one also gets those of the
// user lines in held. Only lines this provider recorded itself keep their
// attachments: the host strips them from every line it loads into a persona
// on Claude or sends it as catch-up, so another persona's images never reach
// Claude.
func (b *Brain) requestImages(held []Turn, attachments []chatmedia.Ref) ([]claude.ContentBlock, error) {
	if len(attachments) > chatmedia.MaxAttachments {
		return nil, errors.New("at most four images may be attached")
	}
	var refs []chatmedia.Ref
	if b.sessionID == "" {
		for _, t := range held {
			if t.Role == "user" {
				refs = append(refs, t.Attachments...)
			}
		}
	}
	var blocks []claude.ContentBlock
	err := eachModelImage(append(refs, attachments...), func(label, mediaType string, data []byte) {
		blocks = append(blocks, claude.TextBlock(label), claude.ImageBlock(mediaType, data))
	})
	if err != nil {
		return nil, err
	}
	return blocks, nil
}

// contentBlocks is a request's prompt followed by its images.
func contentBlocks(prompt string, images []claude.ContentBlock) []claude.ContentBlock {
	return append([]claude.ContentBlock{claude.TextBlock(prompt)}, images...)
}
