package brain

import (
	"context"
	"errors"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

func (o *OllamaBrain) ValidateImages(ctx context.Context, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		return nil
	}
	info, err := o.client.Show(ctx, &api.ShowRequest{Model: o.model})
	if err != nil {
		return fmt.Errorf("check image support: %w", err)
	}
	vision := false
	for _, capability := range info.Capabilities {
		if string(capability) == "vision" {
			vision = true
		}
	}
	if !vision {
		return fmt.Errorf("model %s does not support images; select a vision-capable Ollama model", o.model)
	}
	_, err = loadImageData(refs)
	return err
}
func loadImageData(refs []chatmedia.Ref) ([]api.ImageData, error) {
	if len(refs) > chatmedia.MaxAttachments {
		return nil, errors.New("at most four images may be attached")
	}
	var out []api.ImageData
	for _, r := range refs {
		data, err := chatmedia.DefaultStore().ModelImage(r)
		if err != nil {
			return nil, err
		}
		out = append(out, api.ImageData(data))
	}
	return out, nil
}

// maxRequestImageBytes bounds the images one request carries.
const maxRequestImageBytes = 4 * chatmedia.MaxBytes

// eachModelImage loads the images of one request in order, each once, and
// passes add its label, media type and bytes. Each image may be at most limit
// bytes as sent, and a request carries at most 32 MiB of images.
func eachModelImage(refs []chatmedia.Ref, limit imageLimit, add func(label, mediaType string, data []byte)) error {
	seen := map[string]bool{}
	total := 0
	for _, ref := range refs {
		if seen[ref.ID] {
			continue
		}
		seen[ref.ID] = true
		data, err := limit.load(ref)
		if err != nil {
			return err
		}
		total += len(data)
		if total > maxRequestImageBytes {
			return errors.New("retained images exceed 32 MiB; compact or start a new conversation")
		}
		add(attachedImageLabel(ref), modelImageType(ref), data)
	}
	return nil
}

// imageLimit is the most one image may be as a model receives it. A GIF is
// sent as the PNG of its first frame, which can be larger than the file.
type imageLimit struct {
	bytes int
	// provider names the service whose own limit is lower than the store's.
	provider string
}

var storeImageLimit = imageLimit{bytes: chatmedia.MaxBytes}

// load reads ref as a model receives it and checks it against the limit.
func (l imageLimit) load(ref chatmedia.Ref) ([]byte, error) {
	data, err := chatmedia.DefaultStore().ModelImage(ref)
	if err != nil {
		return nil, err
	}
	if len(data) > l.bytes {
		return nil, l.tooLarge(ref, len(data))
	}
	return data, nil
}

func (l imageLimit) tooLarge(ref chatmedia.Ref, size int) error {
	what := "is"
	if ref.Format == "gif" {
		what = "is, as the PNG of its first frame,"
	}
	to := "images can be sent"
	if l.provider != "" {
		to = "images can be sent to " + l.provider
	}
	return fmt.Errorf("image %s %s %s; at most %s %s", ref.Name, what, mebibytes(size), mebibytes(l.bytes), to)
}

func mebibytes(n int) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// checkImages loads each image in turn and keeps none, so a check holds at
// most one image at a time.
func checkImages(refs []chatmedia.Ref, limit imageLimit) error {
	if len(refs) > chatmedia.MaxAttachments {
		return errors.New("at most four images may be attached")
	}
	for _, ref := range refs {
		if _, err := limit.load(ref); err != nil {
			return err
		}
	}
	return nil
}

// attachedImageLabel is the text block that names the image after it.
func attachedImageLabel(ref chatmedia.Ref) string {
	return fmt.Sprintf("Attached image %s (%s):", ref.ID, ref.Name)
}

// modelImageType is the media type of an image as a model receives it.
func modelImageType(ref chatmedia.Ref) string {
	if ref.Format == "gif" {
		return "image/png"
	}
	return "image/" + ref.Format
}
func (o *OllamaBrain) prepareImageHistory(ctx context.Context, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		for _, turn := range o.working {
			if turn.Role == "user" && len(turn.Attachments) > 0 {
				refs = turn.Attachments
				break
			}
		}
	}
	if err := o.ValidateImages(ctx, refs); err != nil {
		return err
	}
	for i, t := range o.working {
		if t.Role != "user" || len(t.Attachments) == 0 {
			continue
		}
		images, err := loadImageData(t.Attachments)
		if err != nil {
			return err
		}
		if i < len(o.history) {
			o.history[i].Images = images
		}
	}
	return nil
}
func (o *OllamaBrain) attachInputImages(refs []chatmedia.Ref, images []api.ImageData) {
	if len(refs) == 0 {
		return
	}
	o.canonical[len(o.canonical)-1].Attachments = append([]chatmedia.Ref(nil), refs...)
	o.working[len(o.working)-1].Attachments = append([]chatmedia.Ref(nil), refs...)
	o.history[len(o.history)-1].Images = images
}
