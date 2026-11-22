package brain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

// A picture a persona posted rides on the user message after its reply, the
// only role a picture is known to reach the model on. The message also gets a
// line naming the picture and who posted it. Only the prompt history carries
// either: the transcript, the working view and the saved conversation keep
// the picture on the reply.
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

// carriedStates are the words that follow a carried picture's id. They come
// before its name, so a line's state is read without reading the name.
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

// carriedLine matches a whole carried line by its prefix, the picture's id
// (the sha256 of its stored bytes, as chatmedia writes it) and its state.
var carriedLine = regexp.MustCompile(`\nPicture ([0-9a-f]{64}) (attached|no longer attached|not shown, because this model cannot view images|not attached to this request): [^\n]*`)

// withoutCarriedLines is content without the lines about carried pictures.
func withoutCarriedLines(content string) string {
	return carriedLine.ReplaceAllString(content, "")
}

// ageCarriedLines says of each carried picture in content that is not in keep
// that it is no longer attached. One that was never shown because the model
// cannot view images keeps saying so.
func ageCarriedLines(content string, keep map[string]bool) string {
	return carriedLine.ReplaceAllStringFunc(content, func(line string) string {
		m := carriedLine.FindStringSubmatch(line)
		if keep[m[1]] || m[2] == carriedStates[carriedUnseen] {
			return line
		}
		return strings.Replace(line, " "+m[2]+": ", " "+carriedStates[carriedAged]+": ", 1)
	})
}

// carriedLineState is the state of id's line in content, if it has one.
func carriedLineState(content, id string) (carriedState, bool) {
	for _, m := range carriedLine.FindAllStringSubmatch(content, -1) {
		if m[1] != id {
			continue
		}
		for state, words := range carriedStates {
			if words == m[2] {
				return state, true
			}
		}
	}
	return 0, false
}

// withoutCarriedLine is content without id's line.
func withoutCarriedLine(content, id string) string {
	for _, at := range carriedLine.FindAllStringSubmatchIndex(content, -1) {
		if content[at[2]:at[3]] == id {
			return content[:at[0]] + content[at[1]:]
		}
	}
	return content
}

// carriedImages is what the user message at working index j carries that the
// user did not attach: the pictures on the replies since the user message
// before it, then those a persona opened while answering it, each once.
func (o *OllamaBrain) carriedImages(working []Turn, j int) []carriedImage {
	start := j
	for start > 0 && working[start-1].Role != "user" {
		start--
	}
	seen := map[string]bool{}
	for _, ref := range working[j].Attachments {
		seen[ref.ID] = true
	}
	var out []carriedImage
	for _, t := range working[start:j] {
		if t.Role != "assistant" && t.Role != "samantha" {
			continue
		}
		what := "a picture " + o.pictureOwner(t) + " posted"
		for _, ref := range t.Attachments {
			if !seen[ref.ID] {
				seen[ref.ID] = true
				out = append(out, carriedImage{ref: ref, what: what})
			}
		}
	}
	if len(working[j].OpenedImages) > 0 {
		opener := "you"
		for _, t := range working[j+1:] {
			if t.Role == "user" {
				break
			}
			if t.Role == "assistant" || t.Role == "samantha" {
				opener = o.pictureOwner(t)
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

// pictureOwner names who wrote t as the persona answering sees it, by the
// rule projectTurn quotes another speaker's reply by.
func (o *OllamaBrain) pictureOwner(t Turn) string {
	current := o.activePersona()
	if strings.TrimSpace(t.PersonaID) == current && !(current == "" && o.groupModeEnabled()) {
		return "you"
	}
	if name := strings.TrimSpace(t.PersonaDisplayName); name != "" {
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
	for keep > 0 && o.working[keep-1].Role != "user" {
		keep--
	}
	for _, t := range o.working[keep:] {
		if (t.Role == "assistant" || t.Role == "samantha") && len(t.Attachments) > 0 || len(t.OpenedImages) > 0 {
			return true
		}
	}
	return false
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

// carriedShown lists the carried pictures attached on user messages from
// keep on, in order.
func (o *OllamaBrain) carriedShown(keep int) []chatmedia.Ref {
	if len(o.working) != len(o.history) {
		return nil
	}
	var out []chatmedia.Ref
	for j := keep; j < len(o.history); j++ {
		if o.history[j].Role != "user" || o.working[j].Role != "user" {
			continue
		}
		for _, c := range o.carriedImages(o.working, j) {
			if state, ok := carriedLineState(o.history[j].Content, c.ref.ID); ok && state == carriedShown {
				out = append(out, c.ref)
			}
		}
	}
	return out
}

// carryImages gives each user message from keep on the pictures it carries,
// after its own, and a line naming each one. A model that cannot view images
// is sent none. A picture that no longer loads, or does not fit within the
// request's limit once the user's own pictures and reserved bytes are
// counted, is left out with a line saying so, the oldest first. It runs on
// every request and changes nothing that is already as it should be. It
// returns, for each picture it carries, nil when it is attached or why not.
func (o *OllamaBrain) carryImages(keep, reserved int) map[string]error {
	if o.audioResolver != nil || len(o.working) != len(o.history) {
		return nil
	}
	own := make([][]api.ImageData, len(o.history))
	budget := o.imageByteLimit() - reserved
	for j := keep; j < len(o.history); j++ {
		if o.history[j].Role != "user" || o.working[j].Role != "user" {
			continue
		}
		images := o.history[j].Images
		own[j] = images[:min(len(o.working[j].Attachments), len(images)):min(len(o.working[j].Attachments), len(images))]
		for _, image := range own[j] {
			budget -= len(image)
		}
	}
	outcome := map[string]error{}
	full := false
	noRoom := noImageRoom(o.imageByteLimit())
	for j := len(o.history) - 1; j >= keep; j-- {
		m := &o.history[j]
		if m.Role != "user" || o.working[j].Role != "user" {
			continue
		}
		carried := o.carriedImages(o.working, j)
		// Pictures go after the user's own; a message whose own pictures
		// did not all load takes none.
		loaded := len(own[j]) == len(o.working[j].Attachments)
		states := make([]carriedState, len(carried))
		data := make([][]byte, len(carried))
		for k := len(carried) - 1; k >= 0; k-- {
			id := carried[k].ref.ID
			states[k] = carriedAged
			switch {
			case o.imagesWithheld:
				states[k], outcome[id] = carriedUnseen, errImagesWithheld
				continue
			case o.imagesUnchecked:
				states[k], outcome[id] = carriedUnchecked, errImagesUnchecked
				continue
			case !loaded:
				outcome[id] = errOwnImagesUnloaded
				continue
			case full:
				outcome[id] = noRoom
				continue
			}
			image, err := storeImageLimit.load(carried[k].ref)
			if err != nil {
				outcome[id] = err
				continue
			}
			if len(image) > budget {
				full, outcome[id] = true, noRoom
				continue
			}
			data[k], budget, states[k], outcome[id] = image, budget-len(image), carriedShown, nil
		}
		known := map[string]bool{}
		images := own[j]
		for k, c := range carried {
			known[c.ref.ID] = true
			m.Content = setCarriedLine(m.Content, c, states[k])
			if states[k] == carriedShown {
				images = append(images, api.ImageData(data[k]))
			}
		}
		// A line whose reply has left the history can no longer be shown.
		m.Content = ageCarriedLines(m.Content, known)
		if len(images) == 0 {
			images = nil
		}
		m.Images = images
	}
	return outcome
}

// setCarriedLine writes c's line in state in place of the line that names
// the same picture, or after content when there is none.
func setCarriedLine(content string, c carriedImage, state carriedState) string {
	want := c.line(state)
	for _, at := range carriedLine.FindAllStringSubmatchIndex(content, -1) {
		if content[at[2]:at[3]] == c.ref.ID {
			return content[:at[0]] + want + content[at[1]:]
		}
	}
	return content + want
}
