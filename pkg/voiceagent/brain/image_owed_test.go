package brain

import (
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

func TestOwedImageFollowsRequestsAndAttachments(t *testing.T) {
	const request = "No, I mean like show me a picture of some dogs from the internet."
	user := func(lines ...string) []Turn {
		turns := make([]Turn, len(lines))
		for i, line := range lines {
			turns[i] = Turn{Role: "user", Content: line}
		}
		return turns
	}
	after := func(more ...Turn) []Turn { return append(stalledImageConversation(), more...) }
	attached := Turn{Role: "assistant", Content: "Here it is.", Attachments: []chatmedia.Ref{{ID: "delivered"}}}
	announced := Turn{Role: "assistant", Content: stalledImageReply}
	for _, tc := range []struct {
		name    string
		history []Turn
		request string
		idle    int
	}{
		{"request, a job and one turn about something else", stalledImageConversation(), request, 1},
		{"every complaint keeps it open", after(slices.Concat(user(stalledImageFollowups...), []Turn{announced})...), request, 0},
		{"an announcement delivers nothing", after(announced, announced), request, 1},
		{"tool traffic is not conversation", after(Turn{Role: "assistant", ToolCalls: make([]TurnToolCall, 1)}, Turn{Role: "tool", Content: "saved dog.png"}), request, 1},
		{"a slow job outlasts a little conversation", after(user("And another story.", "One more.")...), request, 3},
		{"the request does not follow a change of subject", after(user("And another story.", "One more.", "What time is it?")...), "", 0},
		{"an attachment settles it", after(attached), "", 0},
		{"a later request opens a new one", after(attached, Turn{Role: "user", Content: "Find a photo of a cat."}), "Find a photo of a cat.", 0},
		{"never mind", after(user("Never mind.")...), "", 0},
		{"withdrawn", after(user("Don't add it to the chat.")...), "", 0},
		{"no longer needed", after(user("You don't need to add it anymore.")...), "", 0},
		{"a doubt is not a withdrawal", after(user("Why can't you add it to the chat?")...), request, 0},
		{"pressing is not a withdrawal", after(user(stalledImageFollowups[1])...), request, 0},
		{"pressing is not a withdrawal", after(user("why don't you just add it to the chat")...), request, 0},
		{"pressing is not a withdrawal", after(user("don't stop until you add it to the chat")...), request, 0},
		{"naming the subject asks after it", after(user("where are the dogs")...), request, 0},
		{"asking for the picture again keeps its subject", after(user("Tell me another story.", "Send me the picture already.")...), request, 0},
		{"a picture of something else replaces it", after(user("Send me the picture of a cat.")...), "Send me the picture of a cat.", 0},
		{"a correction replaces the request", after(user("no don't get me a cartoon find me a real photo of a dog")...), "no don't get me a cartoon find me a real photo of a dog", 0},
		{"text for the chat is something else", after(user("Put the weather summary in the chat.")...), request, 2},
		{"a delivery with its own clause is something else", after(user("Write a shopping list and put it in the chat.")...), request, 2},
		{"waiting for something else", after(user("i'm still waiting for my pizza")...), request, 2},
	} {
		owed, ok := FindOwedImage(tc.history)
		if owed.Request != tc.request || ok != (tc.request != "") || owed.Idle != tc.idle {
			t.Errorf("%s: owed=%+v ok=%t", tc.name, owed, ok)
		}
	}
	for _, line := range []string{
		"Don't show me a picture of a monkey.",
		"What does this picture of a monkey mean?",
		"Can you show me what the monkey in this picture is holding?",
		"You just showed me a cat.",
		"How do I share a picture?",
		"Put it in the chat.",
		"What's a good caption for my vacation photo?",
		"Pull up the photos folder.",
		"Download the image processing library.",
		"I can't find the picture you sent.",
	} {
		if owed, ok := FindOwedImage(user(line)); ok {
			t.Errorf("%q opened a request: %+v", line, owed)
		}
	}
}

func TestOwedImageAskedAfter(t *testing.T) {
	owed, ok := FindOwedImage(stalledImageConversation())
	if !ok {
		t.Fatal("no picture is owed")
	}
	for _, line := range append(slices.Clone(stalledImageFollowups),
		"where are the dogs", "send me that", "Put it in the chat.", "why don't you just add it to the chat",
		"You need to search the internet find a nice pair of dogs and add it to the chat",
		"search the internet and find one and add it to the chat") {
		if !owed.AskedAfter(line) {
			t.Errorf("%q does not ask after the picture", line)
		}
	}
	for _, line := range []string{
		"Never mind, don't send it.", "You don't need to add it anymore.", "Why can't you add it to the chat?",
		"Put the weather summary in the chat.", "Write a shopping list and put it in the chat.",
		"search the web for today's weather and add it to the chat", "find the population of paris and put it in the chat",
		"i'm still waiting for my pizza", "Where is it stored?", "Where is the bathroom?", "Tell me a story in the chat.",
	} {
		if owed.AskedAfter(line) {
			t.Errorf("%q asks after the picture", line)
		}
	}
	if (OwedImage{}).AskedAfter("Where is it then?") {
		t.Error("nothing is owed")
	}
}

// A picture request and an unrelated turn share plenty of ordinary words.
func TestOwedImageSubjectIsNotAFunctionWord(t *testing.T) {
	owed, ok := FindOwedImage([]Turn{
		{Role: "user", Content: "Show me a picture of what a quokka looks like."},
		{Role: "assistant", Content: "I can't browse for images right now."},
	})
	if !ok {
		t.Fatal("no picture is owed")
	}
	for _, line := range []string{
		"look up what time the game starts and add it to the chat",
		"Tell me what you think and put it in the chat.",
		"don't you think it looks like rain? look that up and add it to the chat",
		"Where is what I asked about?",
	} {
		if owed.AskedAfter(line) {
			t.Errorf("%q asks after the picture", line)
		}
	}
	for _, line := range []string{"where's the quokka", "find a quokka and add it to the chat", "Where is it then?"} {
		if !owed.AskedAfter(line) {
			t.Errorf("%q does not ask after the picture", line)
		}
	}
}
