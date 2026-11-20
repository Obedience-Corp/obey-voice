package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

// ValidateImages checks new pictures before their turn is sent. A model that
// cannot view them refuses them here, while the draft can still be changed.
func (o *OllamaBrain) ValidateImages(ctx context.Context, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		return nil
	}
	views, err := o.viewsImages(ctx)
	if err != nil {
		return err
	}
	if !views {
		return fmt.Errorf("model %s does not support images; select a vision-capable Ollama model", o.model)
	}
	_, err = loadImageData(refs)
	return err
}

// ValidateAdmittedImages checks the pictures of a turn that is already part of
// the conversation. A model that cannot view them still answers the turn.
func (o *OllamaBrain) ValidateAdmittedImages(ctx context.Context, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		return nil
	}
	views, err := o.viewsImages(ctx)
	if err != nil || !views {
		return err
	}
	_, err = loadImageData(refs)
	return err
}

func (o *OllamaBrain) viewsImages(ctx context.Context) (bool, error) {
	info, err := o.client.Show(ctx, &api.ShowRequest{Model: o.model})
	if err != nil {
		return false, fmt.Errorf("check image support: %w", err)
	}
	for _, capability := range info.Capabilities {
		if string(capability) == "vision" {
			return true, nil
		}
	}
	return false, nil
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

// prepareImageHistory readies the pictures a request carries: this turn's, and
// those of the turns still in the image window. A model that cannot view
// images is sent none. That does not fail the turn: the pictures stay on the
// conversation record for a model that can view them.
func (o *OllamaBrain) prepareImageHistory(ctx context.Context, opts StreamOptions) error {
	keep := turnWindowStart(o.history, imageTurns-1)
	o.imagesWithheld = false
	// current is what a model that views images would be sent with this request.
	var current []chatmedia.Ref
	for i, turn := range o.working {
		if i >= keep && turn.Role == "user" {
			current = append(current, turn.Attachments...)
		}
	}
	current = append(current, opts.Attachments...)
	if len(current) > 0 {
		views, err := o.viewsImages(ctx)
		if err != nil {
			return err
		}
		if !views {
			o.withholdImages()
			if opts.OnImagesWithheld != nil {
				opts.OnImagesWithheld(o.model, current)
			}
			return nil
		}
		if _, err := loadImageData(opts.Attachments); err != nil {
			return err
		}
	}
	for i, t := range o.working {
		if i < keep || t.Role != "user" || len(t.Attachments) == 0 {
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
	// A turn written by the host or ended by a spawn had no reply to trim after.
	o.retireAgedImages()
	return nil
}
func (o *OllamaBrain) attachInputImages(refs []chatmedia.Ref, images []api.ImageData) {
	if len(refs) == 0 {
		return
	}
	o.canonical[len(o.canonical)-1].Attachments = append([]chatmedia.Ref(nil), refs...)
	o.working[len(o.working)-1].Attachments = append([]chatmedia.Ref(nil), refs...)
	if o.imagesWithheld {
		o.history[len(o.history)-1].Content += unseenImageNote(refs)
		return
	}
	o.history[len(o.history)-1].Images = images
}

// withholdImages takes the pictures off every turn of the prompt history and
// says on each that this model was not shown them. The transcript, the
// working view and the attachment store keep every picture.
func (o *OllamaBrain) withholdImages() {
	o.imagesWithheld = true
	for i, t := range o.working {
		if i >= len(o.history) || t.Role != "user" || o.history[i].Role != "user" || len(t.Attachments) == 0 {
			continue
		}
		m := &o.history[i]
		// A recording rides in the same field as a picture.
		if t.Utterance == nil {
			m.Images = nil
		}
		note := unseenImageNote(t.Attachments)
		if !strings.HasSuffix(m.Content, note) && !strings.HasSuffix(m.Content, agedImageNote(t.Attachments)) {
			m.Content += note
		}
	}
}

// unseenImageNote names each picture a turn carries that this model cannot
// view, so the turn still makes sense and the model does not describe it.
func unseenImageNote(refs []chatmedia.Ref) string {
	var b strings.Builder
	for _, ref := range refs {
		b.WriteString("\n" + attachedImageLabel(ref) + " not shown, because this model cannot view images.")
	}
	return b.String()
}

// imageTurns is how many of the most recent user turns keep their images in
// the prompt, counting the turn being answered. An older turn keeps its text
// and says its image is no longer attached, so an image stays about as long
// as it did when the whole turn left the window after two or three turns.
const imageTurns = 3

// retireAgedImages takes the images off user turns that have aged past
// imageTurns. It runs once a reply is in, so the next turn is the third.
// Only the prompt history changes: the transcript, the working view and the
// attachment store keep every image.
func (o *OllamaBrain) retireAgedImages() {
	if o.audioResolver != nil || len(o.working) != len(o.history) {
		return
	}
	keep := turnWindowStart(o.history, imageTurns-1)
	for i := range keep {
		t := o.working[i]
		if t.Role != "user" || len(t.Attachments) == 0 {
			continue
		}
		note := agedImageNote(t.Attachments)
		if !strings.HasSuffix(o.history[i].Content, note) && !strings.HasSuffix(o.history[i].Content, unseenImageNote(t.Attachments)) {
			o.history[i].Content += note
		}
		o.history[i].Images = nil
	}
}

// agedImageNote names each image a turn no longer carries, in the words the
// Claude replay uses for an image it leaves out.
func agedImageNote(refs []chatmedia.Ref) string {
	var b strings.Builder
	for _, ref := range refs {
		b.WriteString("\n" + attachedImageLabel(ref) + " no longer attached.")
	}
	return b.String()
}
