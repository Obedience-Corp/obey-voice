package brain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

// A picture a persona posted rides on the user message after its reply, the
// only role a picture is known to reach the model on. The message also gets a
// line naming the picture and who posted it. Both are added to the request as
// it is built and never stored in the prompt history. The working view notes
// on the user turn which pictures it carries; the transcript and the saved
// conversation keep the picture on the reply only.
type carriedImage struct {
	ref chatmedia.Ref
	// what is whose picture it is, as in "a picture you posted".
	what string
}

type carriedState int

const (
	carriedShown carriedState = iota
	carriedAged
	carriedUnseen
	carriedUnchecked
)

// carriedStates are the words that follow a carried picture's id.
var carriedStates = map[carriedState]string{
	carriedShown:     "attached",
	carriedAged:      "no longer attached",
	carriedUnseen:    "not shown, because this model cannot view images",
	carriedUnchecked: "not attached to this request",
}

// carriedPrefix opens every carried line. A note about the user's own
// pictures starts with "Attached image" and never with this.
const carriedPrefix = "\nPicture "

func (c carriedImage) line(state carriedState) string {
	return carriedPrefix + c.ref.ID + " " + carriedStates[state] + ": " + singleLine(c.ref.Name) + ", " + c.what + "."
}

// singleLine keeps a file name on the one line that names its picture.
func singleLine(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name)
}

// replyPicture is a picture on a reply, with who wrote the reply.
type replyPicture struct {
	ref                    chatmedia.Ref
	personaID, personaName string
}

// postedBefore lists the pictures on the replies between the user turn before
// index end and end, each once, in order. A user turn recorded at end carries
// them from then on, even once those replies leave the working view.
func postedBefore(turns []Turn, end int) []replyPicture {
	start := end
	for start > 0 && turns[start-1].Role != "user" {
		start--
	}
	seen := map[string]bool{}
	var out []replyPicture
	for _, t := range turns[start:end] {
		if t.Role != "assistant" && t.Role != "samantha" {
			continue
		}
		for _, ref := range t.Attachments {
			if !seen[ref.ID] {
				seen[ref.ID] = true
				out = append(out, replyPicture{ref: ref, personaID: t.PersonaID, personaName: t.PersonaDisplayName})
			}
		}
	}
	return out
}

// carriedImages is what the user message at working index j carries that the
// user did not attach: the pictures posted on the replies before it, then
// those a persona opened while answering it, each once.
func (o *OllamaBrain) carriedImages(working []Turn, j int) []carriedImage {
	seen := map[string]bool{}
	for _, ref := range working[j].Attachments {
		seen[ref.ID] = true
	}
	var out []carriedImage
	for _, p := range working[j].posted {
		if !seen[p.ref.ID] {
			seen[p.ref.ID] = true
			out = append(out, carriedImage{ref: p.ref, what: "a picture " + o.pictureOwner(p.personaID, p.personaName) + " posted"})
		}
	}
	if len(working[j].OpenedImages) > 0 {
		opener := "you"
		for _, t := range working[j+1:] {
			if t.Role == "user" {
				break
			}
			if t.Role == "assistant" || t.Role == "samantha" {
				opener = o.pictureOwner(t.PersonaID, t.PersonaDisplayName)
				break
			}
		}
		for _, ref := range working[j].OpenedImages {
			if !seen[ref.ID] {
				seen[ref.ID] = true
				out = append(out, carriedImage{ref: ref, what: "a picture " + opener + " opened"})
			}
		}
	}
	return out
}

// pictureOwner names who wrote a reply as the persona answering sees it, by
// the rule projectTurn quotes another speaker's reply by.
func (o *OllamaBrain) pictureOwner(personaID, personaName string) string {
	current := o.activePersona()
	if strings.TrimSpace(personaID) == current && !(current == "" && o.groupModeEnabled()) {
		return "you"
	}
	if name := strings.TrimSpace(personaName); name != "" {
		return name
	}
	return "another agent"
}

// carriesImagesFrom reports whether a user message from keep on carries a
// picture, or the next one will.
func (o *OllamaBrain) carriesImagesFrom(keep int) bool {
	if o.audioResolver != nil || len(o.working) != len(o.history) || keep > len(o.working) {
		return false
	}
	for _, t := range o.working[keep:] {
		if t.Role == "user" && (len(t.posted) > 0 || len(t.OpenedImages) > 0) {
			return true
		}
	}
	return len(postedBefore(o.working, len(o.working))) > 0
}

// carriedLineBytes is at least the size of the lines the user messages of the
// prompt history are sent with, each counted in its longest wording. It reads
// only the conversation, never the pictures.
func (o *OllamaBrain) carriedLineBytes() int {
	if o.audioResolver != nil || len(o.working) != len(o.history) {
		return 0
	}
	n := 0
	for j, m := range o.history {
		if m.Role != "user" || o.working[j].Role != "user" {
			continue
		}
		for _, c := range o.carriedImages(o.working, j) {
			n += len(c.line(carriedUnseen))
		}
	}
	return n
}

var (
	errImagesWithheld    = errors.New("pictures are left out of this request because this model cannot view images")
	errImagesUnchecked   = errors.New("pictures are left out of this request because whether this model can view images could not be checked")
	errOwnImagesUnloaded = errors.New("the message's own pictures did not load")
)

// imageByteLimit is the most image data one request carries.
func (o *OllamaBrain) imageByteLimit() int {
	if o.maxImageBytes > 0 {
		return o.maxImageBytes
	}
	return maxRequestImageBytes
}

func noImageRoom(limit int) error {
	size := fmt.Sprintf("%d bytes", limit)
	if limit%(1<<20) == 0 {
		size = fmt.Sprintf("%d MiB", limit>>20)
	}
	return fmt.Errorf("this request already carries as much picture data as it can (%s)", size)
}

// requestImageStart is where the image window of the request in progress
// begins: the user message that opens its last imageWindow user messages.
func (o *OllamaBrain) requestImageStart() int {
	return turnWindowStart(o.history, o.imageWindow)
}

type carriedKey struct {
	turn int
	id   string
}

// carriedExtra is what a user message carries besides its own: the lines
// naming its carried pictures and the bytes of those the request attaches.
type carriedExtra struct {
	lines  string
	images []api.ImageData
}

// carried is what each user message of history carries besides its own. Every
// one that carries pictures gets a line naming each of them. Within the image
// window the pictures follow the user's own: a model that cannot view images
// is sent none, and a picture that no longer loads, or does not fit within the
// request's limit once the user's own pictures are counted, is left out with
// a line saying so, the oldest first. outcome holds, for each picture on a
// window message, nil when it is attached or why not.
func (o *OllamaBrain) carried(history []api.Message, working []Turn) (map[int]carriedExtra, map[carriedKey]error) {
	if len(working) != len(history) || o.audioResolver != nil {
		return nil, nil
	}
	keep := turnWindowStart(history, o.imageWindow)
	extras := map[int]carriedExtra{}
	gone := carriedAged
	if o.imagesUnviewable {
		gone = carriedUnseen
	}
	for j := range keep {
		if history[j].Role != "user" || working[j].Role != "user" {
			continue
		}
		var lines strings.Builder
		for _, c := range o.carriedImages(working, j) {
			lines.WriteString(c.line(gone))
		}
		if lines.Len() > 0 {
			extras[j] = carriedExtra{lines: lines.String()}
		}
	}
	budget := o.imageByteLimit()
	for j := keep; j < len(history); j++ {
		if history[j].Role == "user" && working[j].Role == "user" {
			for _, image := range history[j].Images {
				budget -= len(image)
			}
		}
	}
	outcome := map[carriedKey]error{}
	window := map[string]bool{}
	full := false
	noRoom := noImageRoom(o.imageByteLimit())
	for j := len(history) - 1; j >= keep; j-- {
		if history[j].Role != "user" || working[j].Role != "user" {
			continue
		}
		carried := o.carriedImages(working, j)
		if len(carried) == 0 {
			continue
		}
		// Pictures go after the user's own; a message whose own pictures
		// did not all load takes none.
		loaded := len(history[j].Images) == len(working[j].Attachments)
		states := make([]carriedState, len(carried))
		data := make([][]byte, len(carried))
		for k := len(carried) - 1; k >= 0; k-- {
			ref := carried[k].ref
			key := carriedKey{turn: j, id: ref.ID}
			window[ref.ID] = true
			states[k] = carriedAged
			switch {
			case o.imagesWithheld:
				states[k], outcome[key] = carriedUnseen, errImagesWithheld
				continue
			case o.imagesUnchecked:
				states[k], outcome[key] = carriedUnchecked, errImagesUnchecked
				continue
			case !loaded:
				outcome[key] = errOwnImagesUnloaded
				continue
			case full:
				outcome[key] = noRoom
				continue
			}
			image, err := o.carriedBytes(ref)
			if err != nil {
				outcome[key] = err
				continue
			}
			if len(image) > budget {
				full, outcome[key] = true, noRoom
				continue
			}
			data[k], budget, states[k], outcome[key] = image, budget-len(image), carriedShown, nil
		}
		var extra carriedExtra
		var lines strings.Builder
		for k, c := range carried {
			lines.WriteString(c.line(states[k]))
			if states[k] == carriedShown {
				extra.images = append(extra.images, api.ImageData(data[k]))
			}
		}
		extra.lines = lines.String()
		extras[j] = extra
	}
	o.keepCarriedBytes(window)
	return extras, outcome
}

// carriedBytes loads a carried picture as the model receives it, once while
// it stays in the image window.
func (o *OllamaBrain) carriedBytes(ref chatmedia.Ref) ([]byte, error) {
	if data, ok := o.carriedData[ref.ID]; ok {
		return data, nil
	}
	data, err := storeImageLimit.load(ref)
	if err != nil {
		return nil, err
	}
	if o.carriedData == nil {
		o.carriedData = map[string][]byte{}
	}
	o.carriedData[ref.ID] = data
	return data, nil
}

// keepCarriedBytes drops the loaded pictures the image window no longer
// carries.
func (o *OllamaBrain) keepCarriedBytes(window map[string]bool) {
	if len(window) == 0 {
		if o.carriedData != nil {
			o.carriedData = nil
		}
		return
	}
	for id := range o.carriedData {
		if !window[id] {
			delete(o.carriedData, id)
		}
	}
}

// attachedCarried lists the pictures the request attaches to window messages
// besides the user's own, each once, oldest first.
func (o *OllamaBrain) attachedCarried() []chatmedia.Ref {
	_, outcome := o.carried(o.history, o.working)
	seen := map[string]bool{}
	var out []chatmedia.Ref
	for j := o.requestImageStart(); j < len(o.working); j++ {
		if o.working[j].Role != "user" {
			continue
		}
		for _, c := range o.carriedImages(o.working, j) {
			if err, ok := outcome[carriedKey{turn: j, id: c.ref.ID}]; ok && err == nil && !seen[c.ref.ID] {
				seen[c.ref.ID] = true
				out = append(out, c.ref)
			}
		}
	}
	return out
}
