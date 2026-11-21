package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

var imageResultNoun = regexp.MustCompile(`(?i)\b(?:images?|pictures?|photos?|png|jpe?g|gifs?)\b`)
var imageResultClaim = regexp.MustCompile(`(?i)\b(?:i(?: have|'ve|’ve)?|we(?: have|'ve|’ve)?) (?:attached|included|uploaded|sent)\b|\b(?:they(?: are|'re|’re)|it(?: is|'s|’s)|(?:the |these |those )?(?:images?|pictures?|photos?|files?)(?: are| is)?) attached\b`)
var imageResultPromise = regexp.MustCompile(`(?i)\bi(?: will|'ll|’ll| am going to) (?:get|find|fetch|download|attach|send|share|pull)\b`)

// Progress language needs an object: "adding a feature" is conversation,
// "I'm adding it now" is about a deliverable.
var deliveryProgress = regexp.MustCompile(`(?i)(?:^|[.!?,]\s*)(?:(?:i(?: am|'m|’m) )?(?:adding|attaching|uploading|sending)|(?:let me|i(?: will|'ll|’ll)) (?:add|attach|upload|send)) (?:it|them|that|those|(?:the |this |your |an? )?(?:image|photo|picture|file)s?)\b(?: (?:to|in) (?:this|the) chat| now| here|[.!?,]|$)`)
var chatDestination = regexp.MustCompile(`(?i)\b(?:in|to) (?:this|the) chat\b`)
var progressPicture = regexp.MustCompile(`(?i)\b(?:image|photo|picture)s?\b`)
var imageResultQuotation = regexp.MustCompile("`[^`\\n]*`|\"[^\"\\n]*\"|“[^”\\n]*”")
var imageResultEarlier = regexp.MustCompile(`(?i)\b(?:earlier|above|previously|already|last (?:reply|message))\b`)

// Guard explicit image delivery claims/promises on a held response. The current request grounds pronouns such
// as "attached them" without treating unrelated attachment replies as images.
// This is not an intent router: the model must still select the tool
// and effort tier, and normal admission decides whether that work can run.
// Existing image replies are evidence for follow-ups about earlier attachments.
func (o *OllamaBrain) unsupportedImageResult(text, request string) bool {
	prose, ok := o.imageClaimProse(text)
	if !ok {
		return false
	}
	return (imageResultNoun.MatchString(prose) || imageResultNoun.MatchString(request)) && (imageResultClaim.MatchString(prose) || imageResultPromise.MatchString(prose) || promisesChatDelivery(prose))
}

// imageClaimProse is the reply's own wording, without quotations and code.
// It reports false for a reply that needs no claim check.
func (o *OllamaBrain) imageClaimProse(text string) (string, bool) {
	if imageResultEarlier.MatchString(text) {
		for _, turn := range o.canonical {
			if (turn.Role == "assistant" || turn.Role == "samantha") && len(turn.Attachments) > 0 {
				return "", false
			}
		}
	}
	text = chatmedia.ProtectImageExamples(text)
	if chatmedia.ReplyProse(text) != strings.TrimSpace(text) {
		return "", false // Explicit references are validated by the existing importer.
	}
	var prose []string
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if chatmedia.IsFence(line) {
			fenced = !fenced
			continue
		}
		if !fenced && !strings.HasPrefix(line, ">") {
			prose = append(prose, line)
		}
	}
	return imageResultQuotation.ReplaceAllString(strings.Join(prose, "\n"), ""), true
}

// "Let me add it to the chat" and "I'm attaching the picture now" promise a
// delivery here. "Adding it here: <text>" hands the item over, and "adding
// the file now" can be about any file.
func promisesChatDelivery(prose string) bool {
	for _, at := range deliveryProgress.FindAllStringIndex(prose, -1) {
		if strings.HasPrefix(strings.TrimSpace(prose[at[1]:]), ":") {
			continue
		}
		if phrase := prose[at[0]:at[1]]; chatDestination.MatchString(phrase) || progressPicture.MatchString(phrase) {
			return true
		}
	}
	return false
}

var reportsBlocker = regexp.MustCompile(`(?i)\b(?:couldn['’]?t|could not|can['’]?t|cannot|unable|(?:wasn['’]?t|not) able|failed|refused|blocked|denied|don['’]?t have|no (?:access|tool|way))\b`)

// With a picture owed, any further announcement is a stall, whatever it
// calls the picture. A reply that says what went wrong is not one, even
// when it offers to try again.
func (o *OllamaBrain) stalledDelivery(text string) bool {
	prose, ok := o.imageClaimProse(text)
	return ok && !reportsBlocker.MatchString(prose) && (imageResultClaim.MatchString(prose) || imageResultPromise.MatchString(prose) || deliveryProgress.MatchString(prose))
}

var errUnattachedImage = errors.New("the model claimed image delivery without a file reference")

type imageRequestContext uint8

const (
	noImageContext imageRequestContext = iota
	checkVolunteeredImage
	imageMentioned
	owedImageFollowup
)

func (c imageRequestContext) requiresFile(input string) bool {
	return c == owedImageFollowup || (c != noImageContext && requestsPictureDelivery(input))
}

var errMissingImageReference = errors.New("the reply contained no standalone image reference")

// Require a file for explicit delivery requests. Mentions such as "how do I
// share a picture?" and "did you put an image in the chat?" remain discussion.
const imageRequestStart = `(?i)(?:^|[.!?,]\s*)\s*(?:now )?(?:(?:can|could|would|will) you |i (?:really )?(?:need|want) you to )?(?:please )?`

var requestPicture = regexp.MustCompile(imageRequestStart + `(?:show me|send me|attach|put|post|share) (?:an? |the |some |another )?(?:images?|photos?|pictures?|pngs?|jpe?gs?|gifs?)\b`)
var requestPictureInChat = regexp.MustCompile(imageRequestStart + `(?:show me|put|send|post|attach|share)\b[^.!?\n]{0,80}\b(?:in|to) (?:the|this) chat\b`)
var imageFollowup = regexp.MustCompile(`(?i)\b(?:it|them|one|those|that)\b`)

// An immediate follow-up such as "find one online and put it in the chat"
// can refer to the image in the last exchange. Stop at the preceding user.
func (o *OllamaBrain) lastExchangeImage(input string) imageRequestContext {
	if imageResultNoun.MatchString(input) {
		return imageMentioned
	}
	for i := len(o.canonical) - 2; i >= 0; i-- {
		turn := o.canonical[i]
		if turn.Role != "user" && turn.Role != "assistant" && turn.Role != "samantha" || len(turn.ToolCalls) != 0 {
			continue
		}
		if imageResultNoun.MatchString(turn.Content) || len(turn.Attachments) != 0 {
			if imageFollowup.MatchString(input) {
				return imageMentioned
			}
			// The very next turn may be a complaint without an image noun
			// or pronoun. Validate any file the agent volunteers, but let
			// ordinary replies stand without requiring an attachment.
			return checkVolunteeredImage
		}
		if turn.Role == "user" {
			break
		}
	}
	return noImageContext
}

// The second result is the earlier request an owed follow-up refers to.
func (o *OllamaBrain) directImageRequest(input string, opts StreamOptions) (imageRequestContext, string) {
	if !opts.ToolsEnabled || opts.effectiveRouterPolicy() == RouterToolsRouteOnly || opts.OmitTurnInstruction || opts.Background || opts.AgentContinuation || opts.Discussion != nil {
		return noImageContext, ""
	}
	direct := o.lastExchangeImage(input)
	if direct != noImageContext && requestsPictureDelivery(input) {
		return direct, ""
	}
	// "Where is it then?" names the picture only by pronoun, and other turns
	// may sit between it and the request.
	if len(o.canonical) > 1 && !AsksForPicture(input) {
		if owed, ok := FindOwedImage(o.canonical[:len(o.canonical)-1]); ok && owed.AskedAfter(input) {
			return owedImageFollowup, owed.Request
		}
	}
	return direct, ""
}

func requestsPictureDelivery(input string) bool {
	return requestPicture.MatchString(input) || requestPictureInChat.MatchString(input)
}

func (o *OllamaBrain) imageReplyError(text, input string, directRequest imageRequestContext, retried bool) error {
	if directRequest != noImageContext {
		protected := chatmedia.ProtectImageExamples(text)
		if chatmedia.ReplyProse(protected) != strings.TrimSpace(protected) {
			return chatmedia.DefaultStore().CheckReplyFiles(protected, ReplyWorkDir(o))
		}
		if directRequest == owedImageFollowup {
			// The follow-up may be about something else, or the picture may
			// be out of reach. After one correction an honest answer stands;
			// another announcement does not.
			if !retried {
				return errMissingImageReference
			}
			if o.stalledDelivery(text) {
				return errUnattachedImage
			}
			return nil
		}
		if requestsPictureDelivery(input) {
			return errMissingImageReference
		}
		if directRequest == imageMentioned {
			input = "image"
		}
	}
	if o.unsupportedImageResult(text, input) {
		return errUnattachedImage
	}
	return nil
}

func (o *OllamaBrain) imageRepairPrompt(ctx context.Context, directRequest imageRequestContext, owed string, issue error) string {
	if directRequest == noImageContext {
		return systemPrompt(o.cfg, "image-result-repair", nil)
	}
	problem := fmt.Sprintf("%.512s", issue.Error())
	if directRequest == owedImageFollowup {
		return systemPrompt(o.cfg, "owed-image-delivery", map[string]string{
			"request": fmt.Sprintf("%.512s", owed), "work_dir": o.workDir, "import_error": problem,
		})
	}
	paths := []string{}
	if NewBoundary(workerBoundary(o.cfg)).CheckPath(o.workDir, ".") == nil {
		paths = append(paths, chatmedia.DefaultStore().ReplyCandidates(ctx, o.workDir)...)
	}
	candidates, _ := json.Marshal(paths)
	return systemPrompt(o.cfg, "direct-image-repair", map[string]string{
		"work_dir": o.workDir, "import_error": problem, "candidates": string(candidates),
	})
}
