package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// noPictureLine is spoken when a picture request ends without a picture.
const noPictureLine = "I couldn't put a picture in the chat this time."

// ErrNoPictureAttached ends a picture request whose reply carried no picture
// after its retry. The stream has already sent the no-picture line.
var ErrNoPictureAttached = errors.New("no image was attached")

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
	// handoff is the sentences of the failed reply that hand the floor to
	// another persona with an @mention. The host's reply ends with them.
	handoff string
	// none is set when the model chose no file, or chose one its caption
	// contradicts.
	none bool
	// refused names the offered file a refused choice's caption said.
	refused string
}

// choosePicture returns the choice retry for a failed reply, or nil where the
// free-form correction applies: an owed follow-up, a turn that is not the
// speaking persona's live turn, a request that does not ask for a picture to
// be shown, or a work directory with no picture in it or its subfolders.
// The names are listed, not decoded: the chosen file is checked like any
// reply image once the model has answered.
func (o *OllamaBrain) choosePicture(ctx context.Context, input string, direct imageRequestContext, opts StreamOptions) *pictureChoice {
	if direct == noImageContext || direct == owedImageFollowup || !o.answersLive(opts) || !requestsPictureDelivery(input) {
		return nil
	}
	if NewBoundary(workerBoundary(o.cfg)).CheckPath(o.workDir, ".") != nil {
		return nil
	}
	files := chatmedia.PictureNames(ctx, o.workDir)
	if len(files) == 0 {
		return nil
	}
	return &pictureChoice{files: files}
}

// keepHandoff keeps the sentences of the failed reply that @mention another
// persona in the group, so a hand-off asked for with the picture survives the
// retry.
func (c *pictureChoice) keepHandoff(o *OllamaBrain, failed string) {
	own := ""
	if o.cfg != nil {
		own = strings.TrimSpace(o.cfg.AgentName)
	}
	names := map[string]bool{}
	for _, participant := range o.groupRoster() {
		if strings.EqualFold(strings.TrimSpace(participant.Name), own) {
			continue
		}
		for _, name := range append([]string{participant.Name}, participant.Aliases...) {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				names[name] = true
			}
		}
	}
	var kept, mentioned []string
	for _, line := range strings.Split(chatmedia.ReplyProse(failed), "\n") {
		for _, sentence := range lineSentences(line) {
			keep := false
			for _, mention := range textclean.AgentMentions(sentence) {
				if names[strings.ToLower(mention.Target)] {
					keep = true
					mentioned = append(mentioned, mention.Target)
				}
			}
			if keep {
				kept = append(kept, strings.TrimSpace(sentence))
			}
		}
	}
	c.handoff = pictureCaptionText(strings.Join(kept, " "))
	slog.Info("picture reply had no usable picture", "reply_bytes", len(failed), "handoff_to", mentioned)
}

// lineSentences splits a line where the speech chunker ends a sentence, so a
// file name, a decimal or "e.g." stays inside its sentence.
func lineSentences(line string) []string {
	var sentences []string
	at := 0
	for _, end := range SentenceEnds(line) {
		sentences = append(sentences, line[at:end])
		at = end
	}
	if strings.TrimSpace(line[at:]) != "" {
		sentences = append(sentences, line[at:])
	}
	return sentences
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

type choiceField struct {
	Type string   `json:"type"`
	Enum []string `json:"enum,omitempty"`
}

// choiceSchema keeps its properties in this order: Ollama builds the output
// grammar from it, so "wanted" is written before "file".
type choiceSchema struct {
	Type       string `json:"type"`
	Properties struct {
		Wanted  choiceField `json:"wanted"`
		File    choiceField `json:"file"`
		Caption choiceField `json:"caption"`
	} `json:"properties"`
	Required             []string `json:"required"`
	AdditionalProperties bool     `json:"additionalProperties"`
}

// format asks for what the user wanted before the file, and lists "none"
// first, so a small model names the subject before it picks a name.
func (c *pictureChoice) format() json.RawMessage {
	schema := choiceSchema{Type: "object", Required: []string{"wanted", "file", "caption"}}
	schema.Properties.Wanted = choiceField{Type: "string"}
	schema.Properties.File = choiceField{Type: "string", Enum: append([]string{"none"}, c.files...)}
	schema.Properties.Caption = choiceField{Type: "string"}
	out, _ := json.Marshal(schema)
	return out
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
		if c.refused != "" {
			o.emitInfo(events.Info{Message: "The picture was not posted: its caption names " + c.refused + ", not the file chosen"})
		}
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
		// A small model can fill the rest of its output budget with
		// whitespace after the file. The choice is made by then; the host
		// supplies the caption.
		wanted, file, ok := truncatedPictureChoice(response.Message.Content)
		if !ok {
			return "", fmt.Errorf("decode picture choice: %w", err)
		}
		empty := ""
		choice.Wanted, choice.File, choice.Caption = &wanted, &file, &empty
	}
	if choice.Wanted == nil || choice.File == nil || choice.Caption == nil {
		return "", errors.New("the picture choice is missing a field")
	}
	caption := pictureCaptionText(*choice.Caption)
	if *choice.File == "none" {
		slog.Info("picture choice", "file", "none")
		c.none = true
		if !reportsBlocker.MatchString(caption) || imageResultClaim.MatchString(caption) || imageResultPromise.MatchString(caption) {
			caption = noPictureLine
		}
		return c.withHandoff(caption), nil
	}
	if !slices.Contains(c.files, *choice.File) {
		return "", fmt.Errorf("the picture choice named %q, which was not offered", fmt.Sprintf("%.80s", *choice.File))
	}
	other := c.contradicts(caption, *choice.File)
	slog.Info("picture choice", "file", fmt.Sprintf("%.200s", *choice.File), "caption_names", other)
	if other != "" {
		// Treated as no file, so a hand-off asked for with the picture still
		// happens; the picture stays owed and the refusal is shown.
		c.none, c.refused = true, other
		return c.withHandoff(noPictureLine), nil
	}
	if caption == "" {
		caption = pictureCaption
	}
	label := strings.NewReplacer("[", "", "]", "").Replace(path.Base(*choice.File))
	return c.withHandoff(caption + "\n![" + label + "](<" + *choice.File + ">)"), nil
}

// withHandoff ends the host's reply with the failed reply's hand-off, unless
// the caption already @mentions someone.
func (c *pictureChoice) withHandoff(reply string) string {
	if c.handoff == "" || len(textclean.AgentMentions(reply)) > 0 {
		return reply
	}
	return reply + "\n" + c.handoff
}

// genericPictureNames say nothing about what a picture shows, so a caption
// using one does not name that file.
var genericPictureNames = []string{"file", "image", "img", "photo", "pic", "picture", "screenshot"}

// fileWords is a file's name as lowercase words, or nil for a name that says
// nothing about the picture.
func fileWords(file string) []string {
	base := path.Base(file)
	words := strings.FieldsFunc(strings.ToLower(strings.TrimSuffix(base, path.Ext(base))), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 || !strings.ContainsFunc(strings.Join(words, ""), unicode.IsLetter) || (len(words) == 1 && slices.Contains(genericPictureNames, words[0])) {
		return nil
	}
	return words
}

// cameraPrefixes start file names that cameras and phones give pictures.
var cameraPrefixes = []string{"dsc", "dscf", "dscn", "img", "mvimg", "pxl", "scan"}

// opaqueName reports whether a file's name says nothing about the picture,
// as "IMG_2031.jpg" does. The model may have seen it attached, so a caption
// about the beach is no reason to refuse it.
func opaqueName(file string) bool {
	for _, word := range fileWords(file) {
		if len(word) > 2 && !strings.ContainsFunc(word, unicode.IsDigit) && !slices.Contains(cameraPrefixes, word) && !slices.Contains(genericPictureNames, word) {
			return false
		}
	}
	return true
}

// saysWords reports whether text says words in a row, as whole words.
func saysWords(text string, words []string) bool {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = regexp.QuoteMeta(word)
	}
	return regexp.MustCompile(`(?i)(?:^|[^\pL\pN])` + strings.Join(quoted, `[^\pL\pN]+`) + `(?:e?s)?(?:$|[^\pL\pN])`).MatchString(text)
}

// contradicts names an offered file the caption says instead of the chosen
// one, or "": "the dog" said with lion.jpg names dog.jpg. The longest name
// said wins, so "the dog at a laptop" names dog-at-a-laptop.jpg, not dog.jpg,
// and a name within the chosen one's ("the dog" for dog-at-a-laptop.jpg) is
// not a contradiction.
func (c *pictureChoice) contradicts(caption, chosen string) string {
	var named [][]string
	var files []string
	for _, file := range c.files {
		if words := fileWords(file); words != nil && saysWords(caption, words) {
			named, files = append(named, words), append(files, file)
		}
	}
	var said []string
	for i, words := range named {
		longer := false
		for j, other := range named {
			if j != i && len(other) > len(words) && saysWords(strings.Join(other, " "), words) {
				longer = true
			}
		}
		if !longer {
			said = append(said, files[i])
		}
	}
	if slices.Contains(said, chosen) || opaqueName(chosen) {
		return ""
	}
	chosenName := strings.Join(fileWords(chosen), " ")
	for _, file := range said {
		if !saysWords(chosenName, fileWords(file)) {
			return file
		}
	}
	return ""
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

// truncatedPictureChoice reads wanted and file from a choice cut off after
// them.
func truncatedPictureChoice(content string) (wanted, file string, ok bool) {
	dec := json.NewDecoder(strings.NewReader(content))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", "", false
	}
	for _, want := range []string{"wanted", "file"} {
		key, err := dec.Token()
		if err != nil || key != want {
			return "", "", false
		}
		var value string
		if err := dec.Decode(&value); err != nil {
			return "", "", false
		}
		if want == "wanted" {
			wanted = value
		} else {
			file = value
		}
	}
	return wanted, file, true
}
