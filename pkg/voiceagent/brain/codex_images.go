package brain

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

// ValidateImages checks durable input before starting a CLI session. Native
// model capability errors are returned by app-server without mutating history.
func (c *CodexBrain) ValidateImages(ctx context.Context, refs []chatmedia.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return checkImages(refs, storeImageLimit)
}
func (c *CodexBrain) imageInputs(opts StreamOptions) ([]any, error) {
	if len(opts.Attachments) > chatmedia.MaxAttachments {
		return nil, fmt.Errorf("at most four images may be attached")
	}
	var refs []chatmedia.Ref
	if c.sessionID == "" {
		for _, turn := range c.working {
			refs = append(refs, turn.Attachments...)
		}
	}
	refs = append(refs, opts.Attachments...)
	var inputs []any
	err := eachModelImage(refs, storeImageLimit, func(label, mediaType string, data []byte) {
		inputs = append(inputs, map[string]any{"type": "text", "text": label}, map[string]any{"type": "image", "url": "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)})
	})
	if err != nil {
		return nil, err
	}
	return inputs, nil
}
