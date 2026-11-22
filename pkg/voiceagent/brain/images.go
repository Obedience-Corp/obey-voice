package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

// ValidateImages checks pictures for a model that must view them, such as a
// worker handed a picture job. A model that cannot refuses them.
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

// ErrNativeImageInput refuses a picture for a persona that takes native audio.
// Its requests carry recordings where a picture would go, so it can neither
// view a picture nor hold one for another persona: its turn would fail.
var ErrNativeImageInput = errors.New("this persona takes native audio and cannot be sent pictures; select a persona that views images")

// ValidateNewImages checks pictures as they enter the conversation: how many
// there are, and that each one loads. Whether this model can view them is not
// asked. One that cannot answers without them, and they stay on the record
// for a persona that can.
func (o *OllamaBrain) ValidateNewImages(ctx context.Context, refs []chatmedia.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	if o.audioResolver != nil {
		return ErrNativeImageInput
	}
	// Only whether the model can be asked matters here, not its answer: a
	// server that cannot say would fail the turn once the draft is gone.
	if _, err := o.viewsImages(ctx); err != nil {
		return err
	}
	_, err := loadImageData(refs)
	return err
}

// ValidateAdmittedImages checks the pictures of a turn that is already part of
// the conversation. A model that cannot view them still answers the turn.
func (o *OllamaBrain) ValidateAdmittedImages(ctx context.Context, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		return nil
	}
	if o.audioResolver != nil {
		return ErrNativeImageInput
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
	o.imagesWithheld, o.imagesUnchecked = false, false
	// current is what a model that views images would be sent with this
	// request of the pictures the user attached. Pictures from replies are
	// not the user's, so a model that cannot view them is not reported.
	var current []chatmedia.Ref
	for i, turn := range o.working {
		if i >= keep && turn.Role == "user" {
			current = append(current, turn.Attachments...)
		}
	}
	current = append(current, opts.Attachments...)
	reserved := 0
	if len(current) > 0 || o.carriesImagesFrom(keep) {
		views, err := o.viewsImages(ctx)
		if err != nil && len(current) > 0 {
			return err
		}
		if err != nil {
			// Only pictures from replies: leave them out of this request and
			// let the next one that can ask show them.
			o.imagesUnchecked = true
			o.carryImages(keep, 0)
			o.retireAgedImages()
			return nil
		}
		if !views {
			o.withholdImages()
			o.carryImages(keep, 0)
			o.retireAgedImages()
			if len(current) > 0 && opts.OnImagesWithheld != nil {
				opts.OnImagesWithheld(o.model, current)
			}
			return nil
		}
		images, err := loadImageData(opts.Attachments)
		if err != nil {
			return err
		}
		// A group discussion has already recorded its message with these.
		held := map[string]bool{}
		for i, turn := range o.working {
			if i >= keep && turn.Role == "user" {
				for _, ref := range turn.Attachments {
					held[ref.ID] = true
				}
			}
		}
		for i, image := range images {
			if !held[opts.Attachments[i].ID] {
				reserved += len(image)
			}
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
	// A new user message carries the window again once it is appended.
	if opts.RetryInput || opts.Background || opts.AgentContinuation || opts.Discussion != nil {
		o.carryImages(keep, reserved)
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
	for i := range o.history {
		m := &o.history[i]
		if m.Role != "user" {
			continue
		}
		var t Turn
		if i < len(o.working) {
			t = o.working[i]
		}
		// A recording rides in the same field as a picture. Past the working
		// view only a native-audio model can hold one.
		if t.Utterance == nil && (i < len(o.working) || o.audioResolver == nil) {
			m.Images = nil
		}
		if t.Role != "user" || len(t.Attachments) == 0 {
			continue
		}
		note := unseenImageNote(t.Attachments)
		if rest := withoutCarriedLines(m.Content); !strings.HasSuffix(rest, note) && !strings.HasSuffix(rest, agedImageNote(t.Attachments)) {
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

// withoutImageNotes is what the user said in a turn. A note about its pictures
// carries a file name and is not theirs.
func withoutImageNotes(content string, refs []chatmedia.Ref) string {
	content = withoutCarriedLines(content)
	if len(refs) == 0 {
		return content
	}
	content = strings.TrimSuffix(content, unseenImageNote(refs))
	return strings.TrimSuffix(content, agedImageNote(refs))
}

// unviewedTurn is a turn as a provider with no image input receives it: its
// text says the model was not shown the pictures the record names.
func unviewedTurn(t Turn) Turn {
	if t.Role == "user" && len(t.Attachments) > 0 {
		t.Content += unseenImageNote(t.Attachments)
	}
	return t
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
		t, m := o.working[i], &o.history[i]
		if t.Role != "user" || m.Role != "user" {
			continue
		}
		carried := o.carriedImages(o.working, i)
		for _, c := range carried {
			if _, ok := carriedLineState(m.Content, c.ref.ID); !ok {
				m.Content = setCarriedLine(m.Content, c, carriedAged)
			}
		}
		if len(carried) > 0 || carriedLine.MatchString(m.Content) {
			m.Content, m.Images = ageCarriedLines(m.Content, nil), nil
		}
		if len(t.Attachments) == 0 {
			continue
		}
		note := agedImageNote(t.Attachments)
		if rest := withoutCarriedLines(m.Content); !strings.HasSuffix(rest, note) && !strings.HasSuffix(rest, unseenImageNote(t.Attachments)) {
			m.Content += note
		}
		m.Images = nil
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
