package brain

import (
	"context"
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
			Description: "Look at a PNG, JPEG or GIF file inside the work directory. The picture is attached to the user's latest message for your next step; a GIF shows its first frame. read_file cannot show pictures.",
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

// viewImage imports an image file the way a reply image is imported and
// records it on the user turn being answered as a picture the persona opened.
// The turn's next request carries it on that user message.
func (o *OllamaBrain) viewImage(ctx context.Context, path string) string {
	if strings.TrimSpace(path) == "" {
		return "error: path is required"
	}
	if o.audioResolver != nil {
		return cannotViewImages
	}
	views, err := o.viewsImages(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	if !views {
		return cannotViewImages
	}
	c, w := lastUserTurn(o.canonical), lastUserTurn(o.working)
	if c < 0 || w < 0 || o.canonical[c].Content != o.working[w].Content {
		return "error: there is no user message to attach the picture to"
	}
	if len(o.canonical[c].OpenedImages) >= chatmedia.MaxAttachments {
		return "error: at most four pictures can be opened while answering one message"
	}
	ref, err := chatmedia.DefaultStore().ImportWorkFile(path, o.workDir)
	if err != nil {
		return "error: " + err.Error()
	}
	if _, err := storeImageLimit.load(ref); err != nil {
		return "error: " + err.Error()
	}
	label := strings.TrimSuffix(attachedImageLabel(ref), ":")
	for _, held := range append(o.working[w].Attachments, o.working[w].OpenedImages...) {
		if held.ID == ref.ID {
			return label + " is already attached to the user's latest message."
		}
	}
	o.canonical[c].OpenedImages = append(o.canonical[c].OpenedImages, ref)
	o.working[w].OpenedImages = append(o.working[w].OpenedImages, ref)
	if !o.carryImages(turnWindowStart(o.history, imageTurns), 0)[ref.ID] {
		return fmt.Sprintf("error: %s could not be attached to this request, which carries at most 32 MiB of images", label)
	}
	return label + " is now attached to the user's latest message, named there as a picture you opened."
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
