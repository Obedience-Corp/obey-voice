package brain

import (
	"regexp"
	"strings"

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
)

func (c carriedImage) line(state carriedState) string {
	line := "\n" + attachedImageLabel(c.ref) + " " + c.what
	switch state {
	case carriedAged:
		return line + ", no longer attached."
	case carriedUnseen:
		return line + ", not shown, because this model cannot view images."
	}
	return line + "."
}

// carriedLine matches a line written by carriedImage.line in any state. A
// line names its picture, so it can be aged or removed after the reply it
// came from has left the history.
var carriedLine = regexp.MustCompile(`\nAttached image ([0-9a-f]+) \([^\n]*?\): a picture [^\n]*?(, no longer attached|, not shown, because this model cannot view images)?\.(?m:$)`)

// withoutCarriedLines is content without the lines about carried pictures.
func withoutCarriedLines(content string) string {
	return carriedLine.ReplaceAllString(content, "")
}

// ageCarriedLines says of each carried picture in content that is not in keep
// that it is no longer attached.
func ageCarriedLines(content string, keep map[string]bool) string {
	return carriedLine.ReplaceAllStringFunc(content, func(line string) string {
		m := carriedLine.FindStringSubmatch(line)
		if keep[m[1]] {
			return line
		}
		return strings.TrimSuffix(strings.TrimSuffix(line, "."), m[2]) + ", no longer attached."
	})
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

// carryImages gives each user message from keep on the pictures it carries,
// after its own, and a line naming each one. A model that cannot view images
// is sent none. A picture that no longer loads, or does not fit within the
// request's 32 MiB once the user's own pictures and reserved bytes are
// counted, is left out with a line saying so, the oldest first. It runs on
// every request and changes nothing that is already as it should be. It
// returns the pictures it attached.
func (o *OllamaBrain) carryImages(keep, reserved int) map[string]bool {
	if o.audioResolver != nil || len(o.working) != len(o.history) {
		return nil
	}
	own := make([][]api.ImageData, len(o.history))
	budget := maxRequestImageBytes - reserved
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
	shown := map[string]bool{}
	full := false
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
			states[k] = carriedAged
			if o.imagesWithheld {
				states[k] = carriedUnseen
				continue
			}
			if full || !loaded {
				continue
			}
			image, err := storeImageLimit.load(carried[k].ref)
			if err != nil {
				continue
			}
			if len(image) > budget {
				full = true
				continue
			}
			data[k], budget, states[k] = image, budget-len(image), carriedShown
		}
		known := map[string]bool{}
		images := own[j]
		for k, c := range carried {
			known[c.ref.ID] = true
			m.Content = setCarriedLine(m.Content, c, states[k])
			if states[k] == carriedShown {
				images = append(images, api.ImageData(data[k]))
				shown[c.ref.ID] = true
			}
		}
		// A line whose reply has left the history can no longer be shown.
		m.Content = ageCarriedLines(m.Content, known)
		if len(images) == 0 {
			images = nil
		}
		m.Images = images
	}
	return shown
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
