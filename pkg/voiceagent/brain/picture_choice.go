package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

// noPictureLine is spoken when a picture request ends without a picture.
const noPictureLine = "I couldn't put a picture in the chat this time."

// pictureCaption is said with a chosen picture whose caption was empty.
const pictureCaption = "Here's the picture."

// pictureChoiceTokens caps the choice reply: three short strings.
const pictureChoiceTokens = 256

const maxPictureCaption = 300

// errPictureChoice marks a picture choice that could not be used: the request
// failed, or the reply did not decode to one of the offered files.
var errPictureChoice = errors.New("picture choice rejected")

// pictureChoice is the retry of a live picture request whose reply carried no
// usable picture: the model names one verified file in the work directory, or
// none, as JSON, and the host writes the reply.
type pictureChoice struct {
	files []string
	// none is set when the model chose no file.
	none bool
}

// choosePicture returns the choice retry for a failed reply, or nil where the
// free-form correction applies: an owed follow-up, a turn that is not the
// speaking persona's live turn, a request that does not ask for a picture to
// be shown, or a work directory with no usable picture.
func (o *OllamaBrain) choosePicture(ctx context.Context, input string, direct imageRequestContext, opts StreamOptions) *pictureChoice {
	if direct == noImageContext || direct == owedImageFollowup || !o.answersLive(opts) || !requestsPictureDelivery(input) {
		return nil
	}
	files := o.replyCandidates(ctx)
	if len(files) == 0 {
		return nil
	}
	return &pictureChoice{files: files}
}

func (o *OllamaBrain) replyCandidates(ctx context.Context) []string {
	if NewBoundary(workerBoundary(o.cfg)).CheckPath(o.workDir, ".") != nil {
		return nil
	}
	return chatmedia.DefaultStore().ReplyCandidates(ctx, o.workDir)
}

func (c *pictureChoice) prompt(o *OllamaBrain) string {
	files, _ := json.Marshal(c.files)
	return systemPrompt(o.cfg, "picture-choice", map[string]string{"candidates": string(files)})
}

// format asks for what the user wanted before the file, and lists "none"
// first, so a small model names the subject before it picks a name.
func (c *pictureChoice) format() json.RawMessage {
	enum, _ := json.Marshal(append([]string{"none"}, c.files...))
	return json.RawMessage(`{"type":"object","properties":{"wanted":{"type":"string"},"file":{"type":"string","enum":` + string(enum) +
		`},"caption":{"type":"string"}},"required":["wanted","file","caption"],"additionalProperties":false}`)
}

// chat sends the round's messages with the choice schema in place of tools
// and passes on the reply the host wrote from the choice.
func (c *pictureChoice) chat(o *OllamaBrain) func(context.Context, *api.ChatRequest, api.ChatResponseFunc) error {
	return func(ctx context.Context, original *api.ChatRequest, fn api.ChatResponseFunc) error {
		req := *original
		req.Tools = nil
		req.Format = c.format()
		stream := false
		req.Stream = &stream
		req.Think = &api.ThinkValue{Value: false}
		req.Options = maps.Clone(original.Options)
		if req.Options == nil {
			req.Options = map[string]any{}
		}
		req.Options["temperature"] = 0
		capNumPredict(req.Options, pictureChoiceTokens)
		var response api.ChatResponse
		if err := o.chat(ctx, &req, func(resp api.ChatResponse) error {
			response = resp
			return nil
		}); err != nil {
			if ctx.Err() != nil {
				return err
			}
			return fmt.Errorf("%w: %w", errPictureChoice, err)
		}
		text, decodeErr := c.reply(response)
		diagnostics := routeDecisionDiagnostics(response)
		response.Message = api.Message{Role: "assistant", Content: text}
		if decodeErr == nil && response.DoneReason == "length" {
			response.DoneReason = "stop"
		}
		if err := fn(response); err != nil {
			return err
		}
		if decodeErr != nil {
			return fmt.Errorf("%w: %w %s", errPictureChoice, decodeErr, diagnostics)
		}
		return nil
	}
}

// reply is the reply the host writes for a choice: the caption, then the
// chosen file as a reply image named by its file name.
func (c *pictureChoice) reply(response api.ChatResponse) (string, error) {
	if len(response.Message.ToolCalls) != 0 {
		return "", errors.New("the choice returned tool calls")
	}
	var choice struct {
		Wanted  *string `json:"wanted"`
		File    *string `json:"file"`
		Caption *string `json:"caption"`
	}
	if err := json.Unmarshal([]byte(response.Message.Content), &choice); err != nil {
		return "", fmt.Errorf("decode picture choice: %w", err)
	}
	if choice.Wanted == nil || choice.File == nil || choice.Caption == nil {
		return "", errors.New("the picture choice is missing a field")
	}
	caption := pictureCaptionText(*choice.Caption)
	if *choice.File == "none" {
		c.none = true
		if !reportsBlocker.MatchString(caption) || imageResultClaim.MatchString(caption) || imageResultPromise.MatchString(caption) {
			caption = noPictureLine
		}
		return caption, nil
	}
	if !slices.Contains(c.files, *choice.File) {
		return "", fmt.Errorf("the picture choice named %q, which was not offered", fmt.Sprintf("%.80s", *choice.File))
	}
	if caption == "" {
		caption = pictureCaption
	}
	return caption + "\n![" + *choice.File + "](<" + *choice.File + ">)", nil
}

// pictureCaptionText is a caption as one line of prose that cannot itself be
// read as a reply image.
func pictureCaptionText(caption string) string {
	caption = strings.Join(strings.Fields(strings.ReplaceAll(caption, "![", "[")), " ")
	if runes := []rune(caption); len(runes) > maxPictureCaption {
		caption = string(runes[:maxPictureCaption])
	}
	return caption
}

// sayNoPicture ends a picture request that failed its check after the retry
// with a spoken line, recorded as the reply, instead of silence.
func (o *OllamaBrain) sayNoPicture(ctx context.Context, out chan<- string, opts StreamOptions) error {
	if err := sendChunk(ctx, out, noPictureLine); err != nil {
		return err
	}
	o.appendMessage(api.Message{Role: "assistant", Content: noPictureLine}, opts.Identity)
	o.stopped.reply, o.stopped.sent = true, noPictureLine
	o.trimHistory()
	return nil
}
