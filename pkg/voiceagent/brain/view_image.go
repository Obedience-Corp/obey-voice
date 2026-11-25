package brain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

const viewImageToolName = "view_image"

const (
	cannotViewReason = "this model cannot view images"
	cannotViewImages = "error: " + cannotViewReason
)

func viewImageTool() api.Tool {
	return api.Tool{
		Type: "function",
		Function: api.ToolFunction{
			Name:        viewImageToolName,
			Description: "Look at a PNG, JPEG or GIF file inside your work directory. The picture is attached to the user's latest message for your next step; a GIF shows its first frame. read_file cannot show pictures.",
			Parameters: api.ToolFunctionParameters{
				Type:     "object",
				Required: []string{"path"},
				Properties: orderedProps(map[string]api.ToolProperty{
					"path": {Type: api.PropertyType{"string"}, Description: "Image file path."},
				}),
			},
		},
	}
}

// offersViewImage reports whether a turn's tools include view_image: a live,
// foreground turn of the speaking persona with direct tools. Workers and
// requests that answer no new user message do not get it.
func (o *OllamaBrain) offersViewImage(opts StreamOptions) bool {
	return opts.effectiveRouterPolicy() == RouterToolsDirect && (o.cfg == nil || !o.cfg.IsWorker) && opts.workerBudget() == nil &&
		!opts.Background && !opts.AgentContinuation && opts.Discussion == nil && !opts.TransientHistory
}

// viewImage imports an image file inside the work directory and records it on
// the user turn being answered as a picture the persona opened, when this
// request can carry it. The turn's next request carries it on that message.
// Opening again a recorded picture that was pushed out brings it back. It
// reports whether this call attached the picture.
func (o *OllamaBrain) viewImage(ctx context.Context, path string) (string, bool, toolEnd) {
	if strings.TrimSpace(path) == "" {
		return viewFailed("path is required")
	}
	if o.audioResolver != nil {
		return viewFailed(cannotViewReason)
	}
	views, err := o.viewsImages(ctx)
	if err != nil {
		return viewFailed(err.Error())
	}
	if !views {
		return viewFailed(cannotViewReason)
	}
	// This check answered, so pictures need not wait for the next request.
	o.imagesUnchecked = false
	c, w := lastUserTurn(o.canonical), lastUserTurn(o.working)
	if c < 0 || w < 0 || o.canonical[c].Content != o.working[w].Content {
		return viewFailed("there is no user message to attach the picture to")
	}
	ref, err := chatmedia.DefaultStore().ImportWorkFile(path, o.workDir)
	if err != nil {
		return viewFailed(err.Error())
	}
	if _, err := storeImageLimit.load(ref); err != nil {
		return viewFailed(err.Error())
	}
	label := pictureLabel(ref)
	if len(o.working) != len(o.history) {
		return viewFailed(label + " could not be attached: this conversation's prompt history no longer lines up with it")
	}
	notCarried := errors.New("the user's latest message is not part of this request")
	for _, held := range o.working[w].Attachments {
		if held.ID != ref.ID {
			continue
		}
		if len(o.history[w].Images) != len(o.working[w].Attachments) {
			return viewFailed(fmt.Sprintf("%s could not be attached: %v", label, errOwnImagesUnloaded))
		}
		return label + " is already attached to the user's latest message, as a picture the user attached.", false, toolSucceeded()
	}
	_, outcome := o.carried(o.history, o.working)
	reopen := false
	for _, held := range o.carriedImages(o.working, w) {
		if held.ref.ID != ref.ID {
			continue
		}
		why, handled := outcome[carriedKey{turn: w, id: ref.ID}]
		if handled && why == nil {
			return pictureLabel(held.ref) + " is already attached to the user's latest message, where it is named as " + held.what + ".", false, toolSucceeded()
		}
		if !slices.ContainsFunc(o.working[w].OpenedImages, func(r chatmedia.Ref) bool { return r.ID == ref.ID }) {
			if !handled {
				why = notCarried
			}
			return viewFailed(fmt.Sprintf("%s could not be attached: %v", pictureLabel(held.ref), why))
		}
		reopen = true
	}
	if !reopen && len(o.canonical[c].OpenedImages) >= chatmedia.MaxAttachments {
		return viewFailed("at most four pictures can be opened while answering one message")
	}
	before := o.attachedCarried()
	canonicalOpened, workingOpened := o.canonical[c].OpenedImages, o.working[w].OpenedImages
	o.canonical[c].OpenedImages = openedLast(canonicalOpened, ref)
	o.working[w].OpenedImages = openedLast(workingOpened, ref)
	label = pictureLabel(o.working[w].OpenedImages[len(o.working[w].OpenedImages)-1])
	_, outcome = o.carried(o.history, o.working)
	why, handled := outcome[carriedKey{turn: w, id: ref.ID}]
	if !handled {
		why = notCarried
	}
	if why != nil {
		// Only a picture this request carries counts as opened.
		o.canonical[c].OpenedImages, o.working[w].OpenedImages = canonicalOpened, workingOpened
		return viewFailed(fmt.Sprintf("%s could not be attached: %v", label, why))
	}
	after := map[string]bool{}
	for _, held := range o.attachedCarried() {
		after[held.ID] = true
	}
	// A picture the user attached to a message in the window stays attached.
	for j := o.requestImageStart(); j < len(o.working); j++ {
		if o.working[j].Role == "user" && len(o.history[j].Images) == len(o.working[j].Attachments) {
			for _, held := range o.working[j].Attachments {
				after[held.ID] = true
			}
		}
	}
	result := label + " is now attached to the user's latest message, where it is named as a picture you opened."
	for _, earlier := range before {
		if !after[earlier.ID] {
			result += " To make room, " + pictureLabel(earlier) + " is no longer attached."
		}
	}
	return result, true, toolSucceeded()
}

func viewFailed(reason string) (string, bool, toolEnd) {
	text, end := failWith(reason)
	return text, false, end
}

// openedLast is refs with ref, or the entry already there for its picture,
// moved to the end, where it is carried first. refs is left as it was.
func openedLast(refs []chatmedia.Ref, ref chatmedia.Ref) []chatmedia.Ref {
	out := make([]chatmedia.Ref, 0, len(refs)+1)
	for _, held := range refs {
		if held.ID == ref.ID {
			ref = held
			continue
		}
		out = append(out, held)
	}
	return append(out, ref)
}

func pictureLabel(ref chatmedia.Ref) string {
	return "Picture " + ref.ID + " (" + singleLine(ref.Name) + ")"
}

func lastUserTurn(turns []Turn) int {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "user" {
			return i
		}
	}
	return -1
}

// imageFileNote is what read_file says of an image file instead of its bytes.
// It returns "" for a file that is not an image.
func imageFileNote(path string, data []byte, viewable bool) string {
	kind := http.DetectContentType(data)
	if !strings.HasPrefix(kind, "image/") {
		return ""
	}
	note := fmt.Sprintf("%s is an image file (%s, %d bytes), not text.", path, kind, len(data))
	switch kind {
	case "image/png", "image/jpeg", "image/gif":
		if viewable {
			note += " Use view_image to look at it."
		}
	}
	return note
}
