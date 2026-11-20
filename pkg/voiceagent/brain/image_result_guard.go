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

// Require an object for progress language: "adding a feature" is ordinary
// conversation, while "I'm adding it now" is a promise about the deliverable.
var deliveryProgress = regexp.MustCompile(`(?i)(?:^|[.!?,]\s*)(?:(?:i(?: am|'m|’m) )?(?:adding|attaching|uploading|sending)|(?:let me|i(?: will|'ll|’ll)) (?:add|attach|upload|send)) (?:it|them|that|those|(?:the |this |your |an? )?(?:image|photo|picture|file)s?)\b(?: (?:to|in) (?:this|the) chat| now| here|[.!?,]|$)`)
var chatDestination = regexp.MustCompile(`(?i)\b(?:in|to) (?:this|the) chat\b`)
var imageResultQuotation = regexp.MustCompile("`[^`\\n]*`|\"[^\"\\n]*\"|“[^”\\n]*”")
var imageResultEarlier = regexp.MustCompile(`(?i)\b(?:earlier|above|previously|already|last (?:reply|message))\b`)

// Guard explicit image delivery claims/promises on a held response. The current request grounds pronouns such
// as "attached them" without treating unrelated attachment replies as images.
// This is not an intent router: the model must still select the tool
// and effort tier, and normal admission decides whether that work can run.
// Existing image replies are evidence for follow-ups about earlier attachments.
func (o *OllamaBrain) unsupportedImageResult(text, request string) bool {
	if imageResultEarlier.MatchString(text) {
		for _, turn := range o.canonical {
			if (turn.Role == "assistant" || turn.Role == "samantha") && len(turn.Attachments) > 0 {
				return false
			}
		}
	}
	text = chatmedia.ProtectImageExamples(text)
	if chatmedia.ReplyProse(text) != strings.TrimSpace(text) {
		return false // Explicit references are validated by the existing importer.
	}
	text = deliveryProse(text)
	return (imageResultNoun.MatchString(text) || imageResultNoun.MatchString(request)) && (imageResultClaim.MatchString(text) || imageResultPromise.MatchString(text) || deliveryProgress.MatchString(text))
}

func deliveryProse(text string) string {
	var prose []string
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if !fenced && !strings.HasPrefix(line, ">") {
			prose = append(prose, line)
		}
	}
	return imageResultQuotation.ReplaceAllString(strings.Join(prose, "\n"), "")
}

var errUnattachedImage = errors.New("the model claimed image delivery without a file reference")
var errDeliveryPromise = errors.New("the reply promised to add something to chat but delivered nothing")

type imageRequestContext uint8

const (
	noImageContext imageRequestContext = iota
	checkVolunteeredImage
	imageMentioned
	pendingChatDelivery
	pendingImageDelivery
)

func (c imageRequestContext) requiresFile(input string) bool {
	return c == pendingImageDelivery || (c != noImageContext && c != pendingChatDelivery && requestsPictureDelivery(input))
}

// A request for delivery can survive a pronoun-only follow-up. Questions
// about the subject of the image can still be answered in prose.
var asksForDelivery = regexp.MustCompile(`(?i)\b(?:where(?: is|'s|’s) it|where are they)(?: then| now)?\s*(?:[?!.]|$)|\b(?:still waiting|waiting for (?:it|them|you to (?:add|attach|send))|(?:it|they) should be in your next (?:reply|response|message)|(?:add|show|send|attach|put) (?:it|them|that|those))\b`)

var errMissingImageReference = errors.New("the reply contained no standalone image reference")

// Require a file for explicit delivery requests. Mentions such as "how do I
// share a picture?" and "did you put an image in the chat?" remain discussion.
const imageRequestStart = `(?i)(?:^|[.!?,]\s*)\s*(?:now )?(?:(?:can|could|would|will) you |i (?:really )?(?:need|want) you to )?(?:please )?`

var requestPicture = regexp.MustCompile(imageRequestStart + `(?:show me|send me|attach|put|post|share) (?:an? |the |some |another )?(?:images?|photos?|pictures?|pngs?|jpe?gs?|gifs?)\b`)
var requestPictureInChat = regexp.MustCompile(imageRequestStart + `(?:show me|put|send|post|attach|share)\b[^.!?\n]{0,80}\b(?:in|to) (?:the|this) chat\b`)
var imageFollowup = regexp.MustCompile(`(?i)\b(?:it|them|one|those|that)\b`)

// An immediate follow-up such as "find one online and put it in the chat"
// can refer to the image in the last exchange. Stop at the preceding user.
func (o *OllamaBrain) directImageRequest(input string, opts StreamOptions) imageRequestContext {
	if !opts.ToolsEnabled || opts.effectiveRouterPolicy() == RouterToolsRouteOnly || opts.OmitTurnInstruction || opts.Background || opts.AgentContinuation || opts.Discussion != nil {
		return noImageContext
	}
	if imageResultNoun.MatchString(input) {
		return imageMentioned
	}
	// Follow a live exchange about a missing chat item even if both parties
	// refer to it by pronoun. Do not infer the item is an arbitrary local image.
	followup := asksForDelivery.MatchString(input)
	if len(o.canonical) > 1 && (followup || chatDestination.MatchString(input)) {
		if pending := pendingDeliveryContext(o.canonical[:len(o.canonical)-1]); pending != noImageContext {
			if !followup {
				// A new request can also mention the chat. Check an empty
				// promise without requiring the previous image for it.
				return pendingChatDelivery
			}
			return pending
		}
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

func pendingDeliveryContext(history []Turn) imageRequestContext {
	// Walk only the unresolved exchange. Keep the generic chat context if
	// the speaker promised delivery but the original item is ambiguous.
	progress, destination := false, false
exchange:
	for i := len(history) - 1; i >= 0; i-- {
		turn := history[i]
		if len(turn.Attachments) > 0 || len(turn.ToolCalls) > 0 {
			break
		}
		switch turn.Role {
		case "assistant", "samantha":
			prose := deliveryProse(turn.Content)
			if !deliveryProgress.MatchString(prose) {
				break exchange
			}
			progress = true
			destination = destination || chatDestination.MatchString(prose)
		case "user":
			if progress && imageResultNoun.MatchString(turn.Content) {
				return pendingImageDelivery
			}
			destination = destination || chatDestination.MatchString(turn.Content)
			if !asksForDelivery.MatchString(turn.Content) && !imageFollowup.MatchString(turn.Content) {
				break exchange
			}
		}
	}
	if progress && destination {
		return pendingChatDelivery
	}
	return noImageContext
}

func requestsPictureDelivery(input string) bool {
	return requestPicture.MatchString(input) || requestPictureInChat.MatchString(input)
}

func (o *OllamaBrain) imageReplyError(text, input string, directRequest imageRequestContext) error {
	if directRequest != noImageContext {
		protected := chatmedia.ProtectImageExamples(text)
		if chatmedia.ReplyProse(protected) != strings.TrimSpace(protected) {
			return chatmedia.DefaultStore().CheckReplyFiles(protected, ReplyWorkDir(o))
		}
		if (directRequest == pendingImageDelivery || directRequest == pendingChatDelivery) && deliveryProgress.MatchString(deliveryProse(text)) {
			return errDeliveryPromise
		}
		if directRequest == pendingChatDelivery {
			// The item might also be text. A substantive reply or an
			// explanation is valid on an ambiguous follow-up.
			return nil
		}
		if directRequest.requiresFile(input) {
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

func (o *OllamaBrain) imageRepairPrompt(ctx context.Context, directRequest imageRequestContext, issue error) string {
	if directRequest == pendingChatDelivery {
		return systemPrompt(o.cfg, "pending-chat-delivery", nil)
	}
	if directRequest == noImageContext {
		return systemPrompt(o.cfg, "image-result-repair", nil)
	}
	paths := []string{}
	if NewBoundary(workerBoundary(o.cfg)).CheckPath(o.workDir, ".") == nil {
		paths = append(paths, chatmedia.DefaultStore().ReplyCandidates(ctx, o.workDir)...)
	}
	candidates, _ := json.Marshal(paths)
	return systemPrompt(o.cfg, "direct-image-repair", map[string]string{
		"work_dir": o.workDir, "import_error": fmt.Sprintf("%.512s", issue.Error()), "candidates": string(candidates),
	})
}
