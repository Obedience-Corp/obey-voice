package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// renderUserTurn and renderAgentTurn paint finalized turns (ResponseReady and
// session replay). Live streaming uses renderAgentBubble so think and speakable
// stay on separate accumulators.
func renderUserTurn(text string) string {
	return renderUserBubble("You", text)
}

func renderUserBubble(name, text string) string {
	body := userStyle.Render("› "+name) + "\n" + normalStyle.Render(text)
	return userBubbleStyle.Render(body)
}

func renderAgentTurn(name, text string) string {
	return renderAgentTurnAtWidth(name, text, 0)
}

func renderAgentTurnAtWidth(name, text string, width int) string {
	think, spoken := textclean.SplitThink(text)
	showThink := think != ""
	// Tag-only leftover on ResponseReady / session replay: both halves empty
	// means the stored text is think markup with no inner body. Paint the dim
	// state rather than the raw tags. Live frames no longer call this function.
	if !showThink && spoken == "" && strings.TrimSpace(text) != "" {
		showThink = true
	}
	if width > 0 {
		think = ansi.Wrap(think, width, "")
		spoken = ansi.Wrap(spoken, width, "")
	}
	return renderAgentBubble(name, think, spoken, showThink)
}

func renderAgentBubble(name, think, spoken string, showThink bool) string {
	var b strings.Builder
	b.WriteString(agentStyle.Render("● " + name))
	if showThink {
		b.WriteByte('\n')
		b.WriteString(thinkLabelStyle.Render("thinking"))
		if think != "" {
			b.WriteByte('\n')
			b.WriteString(thinkBodyStyle.Render(think))
		}
	}
	if spoken != "" {
		b.WriteByte('\n')
		b.WriteString(renderAgentMentions(spoken))
	}
	return agentBubbleStyle.Render(b.String())
}

func renderAgentMentions(text string) string {
	var b strings.Builder
	last := 0
	for _, mention := range textclean.AgentMentions(text) {
		b.WriteString(text[last:mention.Start])
		b.WriteString(placeholderKnownStyle.Render(text[mention.Start:mention.End]))
		last = mention.End
	}
	b.WriteString(text[last:])
	return normalStyle.Render(b.String())
}
