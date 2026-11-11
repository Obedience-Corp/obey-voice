package brain

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

// maxRequestImages bounds the images one Claude request carries. The API
// applies a stricter size limit to every image of a request with more than 20.
const maxRequestImages = 20

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
// an image for each image it carries, or none. Only the new turn's images,
// attachments, can fail a request. A resumed session already holds earlier
// images; a fresh one is also sent, as far as they fit, those of the user
// lines in held, the newest first, and a label for each one left out. Only
// lines this provider recorded itself keep their attachments: the host strips
// them from every line it loads into a persona on Claude or sends it as
// catch-up, so another persona's images never reach Claude.
func (b *Brain) requestImages(held []Turn, attachments []chatmedia.Ref) ([]claude.ContentBlock, error) {
	if len(attachments) > chatmedia.MaxAttachments {
		return nil, errors.New("at most four images may be attached")
	}
	var fresh []claude.ContentBlock
	used, seen := 0, map[string]bool{}
	err := eachModelImage(attachments, func(label, mediaType string, data []byte) {
		fresh = append(fresh, claude.TextBlock(label), claude.ImageBlock(mediaType, data))
		used += len(data)
	})
	if err != nil {
		return nil, err
	}
	for _, ref := range attachments {
		seen[ref.ID] = true
	}
	if b.sessionID != "" {
		return fresh, nil
	}
	return append(replayImages(held, seen, maxRequestImageBytes-used, maxRequestImages-len(fresh)/2), fresh...), nil
}

// replayImages is the best-effort replay of the images of the user lines in
// held into a fresh session, in their order. Walking from the newest, it keeps
// images while they fit within budget bytes and room images; from the first
// that does not, older ones are left out. An image that no longer loads is
// left out too. Each image left out is named by a label saying it is no longer
// attached, so its line still makes sense. An image in seen is not repeated.
func replayImages(held []Turn, seen map[string]bool, budget, room int) []claude.ContentBlock {
	var refs []chatmedia.Ref
	for _, t := range held {
		if t.Role == "user" {
			refs = append(refs, t.Attachments...)
		}
	}
	data := make([][]byte, len(refs))
	repeat := make([]bool, len(refs))
	full := false
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		if seen[ref.ID] {
			repeat[i] = true
			continue
		}
		seen[ref.ID] = true
		if full || room <= 0 {
			full = true
			continue
		}
		img, err := chatmedia.DefaultStore().ModelImage(ref)
		if err != nil {
			continue
		}
		if len(img) > budget {
			full = true
			continue
		}
		data[i], budget, room = img, budget-len(img), room-1
	}
	var blocks []claude.ContentBlock
	skipped := 0
	for i, ref := range refs {
		switch {
		case repeat[i]:
		case data[i] != nil:
			blocks = append(blocks, claude.TextBlock(attachedImageLabel(ref)), claude.ImageBlock(modelImageType(ref), data[i]))
		default:
			blocks = append(blocks, claude.TextBlock(attachedImageLabel(ref)+" no longer attached."))
			skipped++
		}
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "obey-voice: claude: %d earlier images left out of a fresh session (missing, or past the request's image limits)\n", skipped)
	}
	return blocks
}

// contentBlocks is a request's prompt followed by its images.
func contentBlocks(prompt string, images []claude.ContentBlock) []claude.ContentBlock {
	return append([]claude.ContentBlock{claude.TextBlock(prompt)}, images...)
}

// ReplaysImagesBestEffort reports whether a provider leaves out earlier images
// that no longer load or fit instead of failing the request, so only a
// request's new images need checking before it.
func ReplaysImagesBestEffort(p Provider) bool {
	_, ok := p.(interface{ replaysImagesBestEffort() })
	return ok
}

func (*Brain) replaysImagesBestEffort() {}
