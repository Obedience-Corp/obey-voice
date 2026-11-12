package brain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

// maxRequestImages bounds the images one Claude request carries. The API
// applies a stricter size limit to every image of a request with more than 20.
const maxRequestImages = 20

// claudeImageLimit is the largest image the API accepts: 10 MiB once base64
// encoded, which is 7.5 MiB as sent.
var claudeImageLimit = imageLimit{bytes: 10 << 20 / 4 * 3, provider: "Claude"}

// ValidateImages checks input images before a turn starts, with the limits a
// request applies to them. It reads only the local attachment store.
func (b *Brain) ValidateImages(ctx context.Context, refs []chatmedia.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return checkImages(refs, claudeImageLimit)
}

// requestImages returns the blocks that follow a request's prompt: a label and
// an image for each image it carries, or none. Only the new turn's images,
// attachments, can fail a request. With replay, for a fresh session that is
// not a summary request, it also sends, as far as they fit, those of earlier,
// the attachments of the user lines held, the newest first, and a label for
// each one left out. A resumed session already holds them. Only lines this provider recorded itself keep their
// attachments: the host strips them from every line it loads into a persona
// on Claude or sends it as catch-up, so another persona's images never reach
// Claude.
func requestImages(earlier []chatmedia.Ref, replay bool, attachments []chatmedia.Ref) ([]claude.ContentBlock, error) {
	if len(attachments) > chatmedia.MaxAttachments {
		return nil, errors.New("at most four images may be attached")
	}
	var fresh []claude.ContentBlock
	used, seen := 0, map[string]bool{}
	err := eachModelImage(attachments, claudeImageLimit, func(label, mediaType string, data []byte) {
		fresh = append(fresh, claude.TextBlock(label), claude.ImageBlock(mediaType, data))
		used += len(data)
	})
	if err != nil {
		return nil, err
	}
	for _, ref := range attachments {
		seen[ref.ID] = true
	}
	if !replay {
		return fresh, nil
	}
	return append(replayImages(earlier, seen, maxRequestImageBytes-used, maxRequestImages-len(fresh)/2), fresh...), nil
}

// userAttachments is the attachments of the user lines in held, in order.
func userAttachments(held []Turn) []chatmedia.Ref {
	var refs []chatmedia.Ref
	for _, t := range held {
		if t.Role == "user" {
			refs = append(refs, t.Attachments...)
		}
	}
	return refs
}

// replayImages is the best-effort replay of earlier images into a fresh
// session, in their order. Walking from the newest, it keeps images while they
// fit within budget bytes and room images; from the first that does not, older
// ones are left out. An image that no longer loads, or that is larger than
// Claude accepts, is left out too. Each image left out is named by a label
// saying it is no longer attached, so its line still makes sense. An image in
// seen is not repeated.
func replayImages(refs []chatmedia.Ref, seen map[string]bool, budget, room int) []claude.ContentBlock {
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
		img, err := claudeImageLimit.load(ref)
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

// errImageRefused is the API refusing an image a request carried. The session
// is kept: retrying it fresh would send the same image without the context.
var errImageRefused = errors.New("claude refused an image in this request; send a smaller or different image")

// imageRefusal is err as errImageRefused when the request carried images and
// the CLI reports the API refusing one, or nil. The CLI passes on only the
// API's text, so the match is on that text and kept narrow: a request error
// that names an image. It is not verified against the live service.
func imageRefusal(images []claude.ContentBlock, err error) error {
	if err == nil || len(images) == 0 {
		return nil
	}
	text := err.Error()
	// The SDK keeps a failed CLI's stderr out of its message.
	var cliErr *claude.ClaudeError
	if errors.As(err, &cliErr) {
		if stderr, ok := cliErr.Details["stderr"].(string); ok {
			text += " " + stderr
		}
	}
	text = strings.ToLower(text)
	if !strings.Contains(text, "image") {
		return nil
	}
	for _, sign := range []string{"invalid_request_error", "api error: 400", "could not process image"} {
		if strings.Contains(text, sign) {
			return fmt.Errorf("%w (%w)", errImageRefused, err)
		}
	}
	return nil
}

// contentBlocks is a request's prompt followed by its images. Replayed images
// follow the prompt on purpose: their labels name lines of the conversation
// record the prompt carries.
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
