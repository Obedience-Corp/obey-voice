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
	_, err := loadImageData(refs)
	return err
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
	seen := map[string]bool{}
	total := 0
	for _, ref := range refs {
		if seen[ref.ID] {
			continue
		}
		seen[ref.ID] = true
		data, err := chatmedia.DefaultStore().ModelImage(ref)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > 4*chatmedia.MaxBytes {
			return nil, fmt.Errorf("retained images exceed 32 MiB; compact or start a new conversation")
		}
		mime := "image/" + ref.Format
		if ref.Format == "gif" {
			mime = "image/png"
		}
		inputs = append(inputs, map[string]any{"type": "text", "text": fmt.Sprintf("Attached image %s (%s):", ref.ID, ref.Name)}, map[string]any{"type": "image", "url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)})
	}
	return inputs, nil
}
