package brain

import (
	"regexp"
	"strings"
)

// OwedImage is a picture the user asked for that no reply has delivered.
type OwedImage struct {
	// Request is the user turn that asked for it.
	Request string
	// Idle counts the user turns about something else since the picture was
	// last asked for or asked after.
	Idle int
}

// A background job can outlast a little conversation, but an undelivered
// request does not follow the user through a change of subject.
const owedImageLapse = 3

const pictureWord = `(?:images?|pictures?|photos?|pngs?|jpe?gs?|gifs?)`

// The picture has to be the thing asked for: "find a photo of a dog", not
// "pull up the photos folder" or "download the image processing library".
var asksForPictureRE = regexp.MustCompile(`(?i)\b(?:(?:show|send|give|get|find|fetch|grab) (?:me|us)|find|fetch|download|search for|look for|pull up) (?:an?|some|the|another|more|a few)(?: [\w-]+){0,2} ` + pictureWord +
	`(?:\s*(?:$|[.,!?;:])|\s+(?:of|from|with|that|which|showing|in|on|for|to|and|or|please|here|now|online|again)\b)`)

// "Don't send it" withdraws a request. "Don't stop until you add it" and
// "why don't you just add it" press for it.
var deliveryRefusal = regexp.MustCompile(`(?i)\b(?:don['’]?t|do not|not to|no need to|stop trying to)\s+(?:(?:even|just|bother to|try to|need to|have to)\s+)*(?:add|show|send|attach|put|post|share|get|find|fetch|grab|download|bother)\b`)
var deliveryDismissal = regexp.MustCompile(`(?i)\b(?:never ?mind|forget (?:it|that|about (?:it|that|the))|cancel (?:it|that)|skip (?:it|that))\b`)

// "Why can't you add them?" asks about the delivery without demanding it.
var deliveryDoubt = regexp.MustCompile(`(?i)\b(?:why|how come)\b[^.!?\n]{0,30}\b(?:can['’]?t|cannot|won['’]?t|didn['’]?t|haven['’]?t|aren['’]?t)\b`)

var (
	askedWhere    = regexp.MustCompile(`(?i)\bwhere(?: is|['’]s| are) ([^.!?\n]{1,40}?)(?: then| now| at)?\s*(?:[?!.]|$)`)
	askedWaiting  = regexp.MustCompile(`(?i)\bwaiting\b(?: (?:for|on) ([^.!?\n]{1,40}?))?\s*(?:[.!?,]|$)`)
	askedNext     = regexp.MustCompile(`(?i)\b(?:it|they|that) should be in your next (?:reply|response|message)\b`)
	askedInChat   = regexp.MustCompile(`(?i)\b(?:add|show|send|attach|put|post|share|drop)\b(?: me| us)?(?: ([^.!?\n]{1,60}?))? (?:in|to|into) (?:the|this) (?:\w+ )?chat\b`)
	askedBare     = regexp.MustCompile(`(?i)\b(?:add|show|send|attach|put|post|share)\b(?: me| us)? (it|them|that|those)(?: (?:to me|here|right now|now|already|again|please))*\s*(?:[.!?]|$)`)
	joinedClause  = regexp.MustCompile(`(?i)\b(?:and|then)\s*$`)
	pronounLookup = regexp.MustCompile(`(?i)\b(?:find|get|fetch|grab|show|send|download|look for|search for)(?: me| us)? (?:one|it|them|that|those|some)\b`)
	owedPronoun   = regexp.MustCompile(`^(?:it|them|they|that|those|one|that one|this one)$`)
	owedPicture   = regexp.MustCompile(`^(?:(?:the|my|that|those|an?|some) )?` + pictureWord + `$`)
	subjectWord   = regexp.MustCompile(`[a-z0-9]+`)
)

// Words a picture request and an unrelated turn can share without being
// about the same thing.
var notASubject = func() map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.Fields("the and for you your can could would will please show send give get got find fetch grab download search look pull add attach put post share drop need want mean like some any another more few nice good new old big little real pair one ones from with into onto that this those these them they internet web online google chat here now right just then also too not image images picture pictures photo photos png pngs jpg jpgs jpeg jpegs gif gifs pic pics") {
		words[word] = true
	}
	return words
}()

func subjectWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range subjectWord.FindAllString(strings.ToLower(text), -1) {
		if len(word) < 3 || notASubject[word] {
			continue
		}
		if len(word) > 3 {
			word = strings.TrimSuffix(word, "s")
		}
		words[word] = true
	}
	return words
}

func (o OwedImage) sharesSubject(text string) bool {
	subject := subjectWords(o.Request)
	for word := range subjectWords(text) {
		if subject[word] {
			return true
		}
	}
	return false
}

// names reports whether the object of a follow-up is the owed picture: left
// out, a pronoun, a picture, or the subject the request named.
func (o OwedImage) names(object string) bool {
	object = strings.ToLower(strings.TrimSpace(object))
	return object == "" || owedPronoun.MatchString(object) || owedPicture.MatchString(object) || o.sharesSubject(object)
}

// A delivery joined to its own clause ("write a list and put it in the
// chat") is about that clause unless the clause is about the picture.
func (o OwedImage) deliveryClause(text string, re *regexp.Regexp) bool {
	for from := 0; from < len(text); {
		at := re.FindStringSubmatchIndex(text[from:])
		if at == nil {
			return false
		}
		start, object := from+at[0], ""
		if at[2] >= 0 {
			object = text[from+at[2] : from+at[3]]
		}
		lead := text[:start]
		joined := joinedClause.MatchString(strings.TrimSpace(lead))
		if o.names(object) && (!joined || o.sharesSubject(lead) || pronounLookup.MatchString(lead)) {
			return true
		}
		from = start + 1
	}
	return false
}

func (o OwedImage) refersTo(text string) bool {
	if askedNext.MatchString(text) || o.deliveryClause(text, askedInChat) || o.deliveryClause(text, askedBare) {
		return true
	}
	if at := askedWhere.FindStringSubmatch(text); at != nil && o.names(at[1]) {
		return true
	}
	if at := askedWaiting.FindStringSubmatch(text); at != nil {
		object := strings.ToLower(strings.TrimSpace(at[1]))
		return object == "you" || strings.HasPrefix(object, "you to ") || o.names(object)
	}
	return false
}

// AskedAfter reports whether a user turn asks for the owed picture again or
// asks where it is.
func (o OwedImage) AskedAfter(text string) bool {
	return o.Request != "" && !deliveryRefusal.MatchString(text) && !deliveryDoubt.MatchString(text) && o.refersTo(text)
}

// AsksForPicture reports whether a user turn asks for a picture by name.
// "Put it in the chat" does not: it can be about anything.
func AsksForPicture(text string) bool {
	text = deliveryRefusal.ReplaceAllString(text, " ")
	return requestPicture.MatchString(text) || asksForPictureRE.MatchString(text)
}

// FindOwedImage replays what the user asked for and whether a reply carried
// a file. It never reads the assistant's wording: a reply that announces a
// delivery has delivered nothing until it has an attachment.
func FindOwedImage(history []Turn) (OwedImage, bool) {
	var owed OwedImage
	for _, turn := range history {
		switch turn.Role {
		case "assistant", "samantha":
			if len(turn.Attachments) > 0 {
				owed = OwedImage{}
			}
		case "user":
			text := turn.Content
			if deliveryRefusal.MatchString(text) || deliveryDismissal.MatchString(text) {
				owed = OwedImage{}
			}
			switch {
			case AsksForPicture(text):
				owed = OwedImage{Request: text}
			case owed.Request == "":
			case owed.refersTo(text):
				owed.Idle = 0
			default:
				if owed.Idle++; owed.Idle > owedImageLapse {
					owed = OwedImage{}
				}
			}
		}
	}
	return owed, owed.Request != ""
}
