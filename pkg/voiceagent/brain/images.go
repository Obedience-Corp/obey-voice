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
func (o *OllamaBrain) prepareImageHistory(ctx context.Context, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		for _, turn := range o.working {
			if len(turn.Attachments) > 0 {
				refs = turn.Attachments
				break
			}
		}
	}
	if err := o.ValidateImages(ctx, refs); err != nil {
		return err
	}
	for i, t := range o.working {
		if len(t.Attachments) == 0 {
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
