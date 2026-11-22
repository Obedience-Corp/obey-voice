package brain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

const viewImageToolName = "view_image"

const cannotViewImages = "error: this model cannot view images"

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
// It reports whether the picture was recorded.
func (o *OllamaBrain) viewImage(ctx context.Context, path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "error: path is required", false
	}
	if o.audioResolver != nil {
		return cannotViewImages, false
	}
	views, err := o.viewsImages(ctx)
	if err != nil {
		return "error: " + err.Error(), false
	}
	if !views {
		return cannotViewImages, false
	}
	// This check answered, so pictures need not wait for the next request.
	o.imagesUnchecked = false
	c, w := lastUserTurn(o.canonical), lastUserTurn(o.working)
	if c < 0 || w < 0 || o.canonical[c].Content != o.working[w].Content {
		return "error: there is no user message to attach the picture to", false
	}
	if len(o.canonical[c].OpenedImages) >= chatmedia.MaxAttachments {
		return "error: at most four pictures can be opened while answering one message", false
	}
	ref, err := chatmedia.DefaultStore().ImportWorkFile(path, o.workDir)
	if err != nil {
		return "error: " + err.Error(), false
	}
	if _, err := storeImageLimit.load(ref); err != nil {
		return "error: " + err.Error(), false
	}
	label := pictureLabel(ref)
	if len(o.working) != len(o.history) {
		return "error: " + label + " could not be attached: this conversation's prompt history no longer lines up with it", false
	}
	for _, held := range o.working[w].Attachments {
		if held.ID == ref.ID {
			return label + " is already attached to the user's latest message, as a picture the user attached.", false
		}
	}
	for _, held := range o.carriedImages(o.working, w) {
		if held.ref.ID == ref.ID {
			return pictureLabel(held.ref) + " is already attached to the user's latest message, where it is named as " + held.what + ".", false
		}
	}
	keep := o.requestImageStart()
	before := o.carriedShown(keep)
	o.canonical[c].OpenedImages = append(o.canonical[c].OpenedImages, ref)
	o.working[w].OpenedImages = append(o.working[w].OpenedImages, ref)
	outcome := o.carryImages(keep, 0)
	why, handled := outcome[ref.ID]
	if !handled {
		why = errors.New("the user's latest message is not part of this request")
	}
	if why != nil {
		// Only a picture this request carries counts as opened.
		o.canonical[c].OpenedImages = o.canonical[c].OpenedImages[:len(o.canonical[c].OpenedImages)-1]
		o.working[w].OpenedImages = o.working[w].OpenedImages[:len(o.working[w].OpenedImages)-1]
		o.history[w].Content = withoutCarriedLine(o.history[w].Content, ref.ID)
		o.carryImages(keep, 0)
		return fmt.Sprintf("error: %s could not be attached: %v", label, why), false
	}
	result := label + " is now attached to the user's latest message, where it is named as a picture you opened."
	for _, earlier := range before {
		if outcome[earlier.ID] != nil {
			result += " To make room, " + pictureLabel(earlier) + " is no longer attached."
		}
	}
	return result, true
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
