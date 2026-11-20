package brain

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

const missingDogImages = "I searched for a dog image and found a suitable PNG and GIF. I’ve attached them below."

func TestUnsupportedImageResult(t *testing.T) {
	o := &OllamaBrain{}
	for _, text := range []string{
		missingDogImages,
		"They're attached here: a PNG file and a GIF file located in the working directory.",
		"I'll get those PNG images attached and send them over.",
		"I attached a PNG.\n```markdown\n![Example](</not-a-result.png>)\n```",
	} {
		if !o.unsupportedImageResult(text, "") {
			t.Errorf("accepted missing image: %q", text)
		}
	}
	for _, text := range []string{
		"I couldn't attach an image because the download failed.",
		"PNG is a lossless image format.",
		"I attached a PDF.",
		"I can attach an image if you want one.",
		"The phrase “I attached a PNG” is an example.",
		"```text\nI attached a PNG.\n```",
		"> I attached a PNG.\nThat claim was wrong.",
		"I attached the image.\n![Dog](</work/dog.png>)",
	} {
		if o.unsupportedImageResult(text, "") {
			t.Errorf("rejected ordinary or explicit reply: %q", text)
		}
	}
	o.canonical = []Turn{{Role: "assistant", Attachments: []chatmedia.Ref{{ID: "existing"}}}}
	if o.unsupportedImageResult("I attached a PNG earlier.", "") {
		t.Fatal("ignored existing attachment evidence")
	}
	if !o.unsupportedImageResult(missingDogImages, "") {
		t.Fatal("an earlier image excused a new unsupported attachment claim")
	}
}

func imageClaimBrain(t *testing.T, replies []api.Message) (*OllamaBrain, *[]api.ChatRequest) {
	t.Helper()
	var requests []api.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, req)
		if len(requests) > len(replies) {
			t.Error("unbounded image correction")
			http.Error(w, "too many calls", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: replies[len(requests)-1], Done: true, Metrics: api.Metrics{PromptEvalCount: 10, EvalCount: 5}})
	}))
	t.Cleanup(srv.Close)
	host, _ := url.Parse(srv.URL)
	cfg := &config.Config{AgentName: "Obey", MaxHistory: 20, VoiceToolsEnabled: true, PromptsDir: t.TempDir()}
	o := &OllamaBrain{client: api.NewClient(host, srv.Client()), model: "fixture", cfg: cfg}
	return o, &requests
}

func TestImageClaimRepairRoutesBeforeSpeechAndKeepsTier(t *testing.T) {
	o, requests := imageClaimBrain(t, []api.Message{
		{Role: "assistant", Content: "I’ve attached them below."},
		{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "spawn", "tier": "medium", "reason_code": "dependent_steps"})}}}},
	})
	usage := 0
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, WorkerTiers: &WorkerTierAvailability{Medium: true}, OnUsage: func(int, int) { usage++ }}
	stream, err := o.ThinkStream(t.Context(), "Find a PNG of a dog and show it here.", opts)
	if err != nil {
		t.Fatal(err)
	}
	chunks, _ := drainStream(t, stream)
	result := <-stream.Done
	if result.Err != nil || result.Route != "spawn" || result.RouteTier != config.WorkerTierMedium || result.RouteReasonCode != "dependent_steps" {
		t.Fatalf("repair lost route or tier: %+v", result)
	}
	if chunks != "" || len(o.History()) != 0 {
		t.Fatalf("unverified claim reached speech/history: %q %+v", chunks, o.History())
	}
	if len(*requests) != 2 || usage != 2 || len((*requests)[1].Tools) == 0 {
		t.Fatalf("repair accounting: requests=%d usage=%d", len(*requests), usage)
	}
	if !strings.Contains((*requests)[1].Messages[0].Content, "Nothing was delivered to the chat") {
		t.Fatal("missing correction instruction")
	}
}

func TestImageClaimRepairIsBoundedAndAllowsHonestFailure(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, honest := range []bool{false, true} {
			second := missingDogImages
			if honest {
				second = "I couldn't obtain the image, so nothing was attached."
			}
			replies := []api.Message{{Role: "assistant", Content: missingDogImages}}
			if honest {
				// Even an honest refusal must satisfy main's route-first contract.
				replies = append(replies, api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "chat"})}}}})
			}
			replies = append(replies, api.Message{Role: "assistant", Content: second})
			o, requests := imageClaimBrain(t, replies)
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
			var reply string
			var err error
			if streaming {
				stream, startErr := o.ThinkStream(t.Context(), "Show me a dog image.", opts)
				if startErr != nil {
					t.Fatal(startErr)
				}
				reply, _ = drainStream(t, stream)
				err = (<-stream.Done).Err
			} else {
				reply, err = o.ThinkFull(t.Context(), "Show me a dog image.", opts)
			}
			if len(*requests) != len(replies) {
				t.Fatalf("requests=%d", len(*requests))
			}
			if honest {
				if err != nil || reply != second {
					t.Fatalf("honest failure: %q %v", reply, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "no image was attached") || reply != "" {
				t.Fatalf("unverified claim escaped: %q %v", reply, err)
			}
			for _, turn := range o.History() {
				if strings.Contains(turn.Content, missingDogImages) {
					t.Fatal("rejected claim polluted history")
				}
			}
		}
	}
}

func TestImageAndMissingRouteShareOneRetry(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, imageFirst := range []bool{false, true} {
			first, second := "I'll look that up.", missingDogImages
			if imageFirst {
				first, second = second, first
			}
			o, requests := imageClaimBrain(t, []api.Message{{Role: "assistant", Content: first}, {Role: "assistant", Content: second}})
			usage := 0
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, OnUsage: func(int, int) { usage++ }}
			var reply string
			var err error
			if streaming {
				stream, startErr := o.ThinkStream(t.Context(), "Find an image.", opts)
				if startErr != nil {
					t.Fatal(startErr)
				}
				reply, _ = drainStream(t, stream)
				err = (<-stream.Done).Err
			} else {
				reply, err = o.ThinkFull(t.Context(), "Find an image.", opts)
			}
			if err == nil || reply != "" || len(*requests) != 2 || usage != 2 {
				t.Fatalf("streaming=%t imageFirst=%t reply=%q error=%v requests=%d usage=%d", streaming, imageFirst, reply, err, len(*requests), usage)
			}
		}
	}
}

func TestImageClaimAfterChatRoute(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range []struct {
			name, input, answer string
			blocked             bool
		}{
			{"named image", "Show me a dog image.", "I attached a dog image.", true},
			{"pronoun", "Find a PNG of a dog.", "I’ve attached them below.", true},
			{"refusal", "Show me a dog image.", "I couldn't obtain it, so nothing was attached.", false},
			{"explicit reference", "Show me a dog image.", "I attached it.\n![Dog](</work/dog.png>)", false},
			{"unrelated attachment", "Where is the PDF?", "I’ve attached it below.", false},
			{"quoted claim", "Explain this image caption.", "The phrase “I’ve attached them below” is not proof.", false},
		} {
			t.Run(tc.name+map[bool]string{false: "/blocking", true: "/streaming"}[streaming], func(t *testing.T) {
				o, requests := imageClaimBrain(t, []api.Message{
					{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "chat"})}}}},
					{Role: "assistant", Content: tc.answer},
				})
				usage := 0
				opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, OnUsage: func(int, int) { usage++ }}
				var reply string
				var err error
				if streaming {
					stream, startErr := o.ThinkStream(t.Context(), tc.input, opts)
					if startErr != nil {
						t.Fatal(startErr)
					}
					reply, _ = drainStream(t, stream)
					err = (<-stream.Done).Err
				} else {
					reply, err = o.ThinkFull(t.Context(), tc.input, opts)
				}
				if len(*requests) != 2 || usage != 2 || len((*requests)[1].Tools) != 0 {
					t.Fatalf("post-chat accounting: requests=%d usage=%d", len(*requests), usage)
				}
				if tc.blocked {
					if err == nil || !strings.Contains(err.Error(), "no image was attached") || reply != "" {
						t.Fatalf("unsupported claim escaped: reply=%q error=%v", reply, err)
					}
					for _, turn := range o.History() {
						if strings.Contains(turn.Content, tc.answer) {
							t.Fatal("unsupported claim entered history")
						}
					}
				} else if err != nil || reply != tc.answer {
					t.Fatalf("valid answer rejected: reply=%q error=%v", reply, err)
				}
			})
		}
	}
}

func TestDirectTextWriteCannotFakeImageDelivery(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		root := t.TempDir()
		path := filepath.Join(root, "result.png")
		if err := os.WriteFile(path, testmedia.PNG(), 0600); err != nil {
			t.Fatal(err)
		}
		const claim = "I attached it — now look."
		o, requests := imageClaimBrain(t, []api.Message{
			{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "result.png", "content": "![picture](result.png)"})}}}},
			{Role: "assistant", Content: claim},
			{Role: "assistant", Content: claim},
		})
		o.workDir = root
		opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect}
		const input = "Please put a picture in the chat."
		var reply string
		var err error
		if streaming {
			stream, startErr := o.ThinkStream(t.Context(), input, opts)
			if startErr != nil {
				t.Fatal(startErr)
			}
			reply, _ = drainStream(t, stream)
			err = (<-stream.Done).Err
		} else {
			reply, err = o.ThinkFull(t.Context(), input, opts)
		}
		if err == nil || !strings.Contains(err.Error(), "no image was attached") || reply != "" || len(*requests) != 3 {
			t.Fatalf("streaming=%t: reply=%q err=%v requests=%d", streaming, reply, err, len(*requests))
		}
		if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, testmedia.PNG()) {
			t.Fatal("text write replaced an existing picture")
		}
		for _, turn := range o.History() {
			if turn.Content == claim {
				t.Fatal("unsupported claim retained in history")
			}
		}
	}
}

func TestImageRouteGuidanceRequiresAvailableWorker(t *testing.T) {
	for _, tiers := range []*WorkerTierAvailability{nil, {}, {Simple: true}, {Medium: true}, {Complex: true}} {
		want := tiers == nil || *tiers != (WorkerTierAvailability{})
		description := liveRouterTools(tiers)[0].Function.Description
		if strings.Contains(description, "attach a new picture") != want {
			t.Errorf("tiers=%+v description=%q", tiers, description)
		}
	}
}

func TestPictureDiscussionStaysInChat(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref, err := chatmedia.DefaultStore().Import("dog.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input, answer string
		attachments   []chatmedia.Ref
	}{
		{"What color is the dog in the picture I attached?", "The dog is brown.", []chatmedia.Ref{ref}},
		{"That picture reminds me of home.", "What does it remind you of?", nil},
	} {
		fake := &jsonRouteFake{native: map[string]string{"kind": "chat"}, answer: tc.answer}
		fake.intercept = func(w http.ResponseWriter, r *http.Request, req api.ChatRequest) bool {
			if r.URL.Path == "/api/show" {
				_, _ = w.Write([]byte(`{"capabilities":["vision","tools"]}`))
				return true
			}
			if hasTool(req.Tools, "route") && len(tc.attachments) > 0 {
				images := req.Messages[len(req.Messages)-1].Images
				if len(images) != 1 || !bytes.Equal(images[0], testmedia.PNG()) {
					t.Error("route request omitted the attached picture")
				}
			}
			return false
		}
		o := jsonRouteTestBrain(t, fake.server(t))
		turn := runRoutedTurn(t, o, tc.input, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, Attachments: tc.attachments})
		if turn.result.Err != nil || turn.result.Route != "" || turn.answer != tc.answer || len(turn.tools) != 1 || turn.tools[0].Summary != "chat" {
			t.Errorf("%q: answer=%q result=%+v tools=%+v", tc.input, turn.answer, turn.result, turn.tools)
		}
	}
}

func TestDirectImageComplaintChecksVolunteeredPicture(t *testing.T) {
	const complaint = "What the hell's going on with you? You're not doing anything."
	const request = "Show me a picture in the chat."
	const bad = "Alright, here you go.\n![cat photo](cat.jpg)"
	for _, streaming := range []bool{true, false} {
		o, requests := imageClaimBrain(t, []api.Message{{Role: "assistant", Content: bad}, {Role: "assistant", Content: "Here is the picture.\n![Picture](valid.png)"}})
		root := t.TempDir()
		o.workDir, o.cfg.WorkDir = root, root
		for name, data := range map[string][]byte{"cat.jpg": []byte("Bad gateway"), "valid.png": testmedia.PNG()} {
			if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := o.ImportHistory([]Turn{{Role: "user", Content: request}}); err != nil {
			t.Fatal(err)
		}
		opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect}
		var reply string
		var err error
		if streaming {
			stream, startErr := o.ThinkStream(t.Context(), complaint, opts)
			if startErr != nil {
				t.Fatal(startErr)
			}
			reply, _ = drainStream(t, stream)
			err = (<-stream.Done).Err
		} else {
			reply, err = o.ThinkFull(t.Context(), complaint, opts)
		}
		if err != nil || !strings.Contains(reply, "valid.png") || strings.Contains(reply, "cat.jpg") || len(*requests) != 2 {
			t.Fatalf("streaming=%t reply=%q requests=%d err=%v", streaming, reply, len(*requests), err)
		}
		messages := (*requests)[1].Messages
		if msg := messages[len(messages)-1].Content; !strings.Contains(msg, "valid.png") || !strings.Contains(msg, "cat.jpg") {
			t.Fatalf("missing repair feedback: %q", msg)
		}
	}
}

// The turn order and the wording each pattern depends on follow a recorded
// conversation. Only the subject is changed.
func stalledImageConversation() []Turn {
	return []Turn{
		{Role: "user", Content: "No, I mean like show me a picture of some dogs from the internet."},
		{Role: "assistant", Content: "Alright, let me find something nice for you."},
		{Role: "user", Content: "I'll do it."},
		{Role: "assistant", Content: "Just wait."},
		{Role: "user", Content: "You need to search the internet find a nice pair of dogs and add it to the chat"},
		{Role: "assistant", Content: "I'll work on that and report back."},
		{Role: "user", Content: "Alright, well a lot of it's working on there. Uh can you tell me some stories?"},
		{Role: "assistant", Content: "I'll tell you a story, but only if you promise to listen."},
		{Role: "assistant", Content: "I found a nice pair of dogs for you. Let me add it to the chat.", ResponseID: "work-first"},
	}
}

var stalledImageFollowups = []string{
	"Alright, I'm waiting for you to add to the chat.",
	"Add the dogs to the chat right now. That should be the next thing you do. Don't stop until you have it done.",
	"No you're not, you just keep saying you're adding it, add it to the damn chat",
	"Where is it then?",
	"It should be in your next response",
}

const stalledImageReply = "I’m adding it now, baby. Just wait for the rush."

type directTurn struct {
	answer   string
	err      error
	requests []api.ChatRequest
	history  []Turn
}

func runDirectTurn(t *testing.T, streaming bool, history []Turn, input string, replies ...string) directTurn {
	t.Helper()
	messages := make([]api.Message, len(replies))
	for i, reply := range replies {
		messages[i] = api.Message{Role: "assistant", Content: reply}
	}
	o, requests := imageClaimBrain(t, messages)
	if err := o.ImportHistory(history); err != nil {
		t.Fatal(err)
	}
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect}
	var out directTurn
	if streaming {
		turn := runRoutedTurn(t, o, input, opts)
		out.answer, out.err = turn.answer, turn.result.Err
	} else {
		out.answer, out.err = o.ThinkFull(t.Context(), input, opts)
	}
	out.requests, out.history = *requests, o.History()
	return out
}

func TestOwedPictureFollowupIsNotAnsweredWithAnAnnouncement(t *testing.T) {
	for i, input := range stalledImageFollowups {
		history := stalledImageConversation()
		if i == len(stalledImageFollowups)-1 {
			// Earlier complaints and the announcements they drew do not settle the request.
			for _, earlier := range stalledImageFollowups[:i] {
				history = append(history, Turn{Role: "user", Content: earlier}, Turn{Role: "assistant", Content: stalledImageReply})
			}
		}
		for _, streaming := range []bool{false, true} {
			for _, retry := range []string{stalledImageReply, "I've attached it.", "I'll send it right away."} {
				turn := runDirectTurn(t, streaming, history, input, stalledImageReply, retry)
				if !errors.Is(turn.err, errUnattachedImage) || turn.answer != "" || len(turn.requests) != 2 {
					t.Fatalf("%q streaming=%t retry=%q: answer=%q err=%v requests=%d", input, streaming, retry, turn.answer, turn.err, len(turn.requests))
				}
				correction := turn.requests[1].Messages[len(turn.requests[1].Messages)-1].Content
				if !strings.Contains(correction, "show me a picture of some dogs from the internet") {
					t.Fatalf("%q: the correction does not name the picture that is owed: %s", input, correction)
				}
				if got := len(turn.history); got > len(history)+1 {
					t.Fatalf("%q: an announcement was saved as a reply: %+v", input, turn.history[len(history):])
				}
			}
		}
	}
}

func TestOwedPictureFollowupAcceptsAnHonestAnswerAfterOneCorrection(t *testing.T) {
	for _, blocker := range []string{
		"I couldn't get one. The download was refused.",
		"I couldn't get one, the download was refused. I'll find another source if you want.",
	} {
		for _, input := range []string{"Where is it then?", "Put it in the chat.", "Add it to the chat."} {
			for _, streaming := range []bool{false, true} {
				for _, first := range []string{stalledImageReply, blocker} {
					turn := runDirectTurn(t, streaming, stalledImageConversation(), input, first, blocker)
					if turn.err != nil || turn.answer != blocker || len(turn.requests) != 2 {
						t.Fatalf("%q streaming=%t first=%q: answer=%q err=%v requests=%d", input, streaming, first, turn.answer, turn.err, len(turn.requests))
					}
				}
			}
		}
	}
}

func TestOwedPictureDoesNotFollowACancelADeliveryOrANewSubject(t *testing.T) {
	promised := []Turn{
		{Role: "user", Content: "Show me a picture of a dog."},
		{Role: "assistant", Content: "Let me add it to the chat."},
	}
	delivered := []Turn{
		{Role: "user", Content: "Show me a picture of a dog."},
		{Role: "assistant", Content: "Here it is.", Attachments: []chatmedia.Ref{{ID: "delivered"}}},
	}
	weather := []Turn{
		{Role: "user", Content: "Show me a picture of a dog."},
		{Role: "assistant", Content: "Sure, give me a moment."},
		{Role: "user", Content: "What's the weather in Paris?"},
		{Role: "assistant", Content: "Sunny, twenty degrees."},
	}
	haiku := append(slices.Clone(promised),
		Turn{Role: "user", Content: "Actually forget that, write me a haiku instead."},
		Turn{Role: "assistant", Content: "Let me add it to the chat."})
	for _, tc := range []struct {
		name    string
		history []Turn
		input   string
		reply   string
	}{
		{"cancel", stalledImageConversation(), "Never mind, don't send it.", "Okay, I won't."},
		{"cancel", promised, "Never mind, don't send it.", "Okay, I won't."},
		{"no longer needed", stalledImageConversation(), "You don't need to add it anymore.", "Understood."},
		{"another subject", stalledImageConversation(), "Tell me another story.", "Once upon a time, a dog found a home."},
		{"another subject in the chat", promised, "Tell me a story in the chat.", "Once upon a time, a dog found a home."},
		{"text for the chat", weather, "Put the weather summary in the chat.", "Sunny, twenty degrees."},
		{"text for the chat", stalledImageConversation(), "Put the weather summary in the chat.", "Sunny, twenty degrees."},
		{"its own clause", stalledImageConversation(), "Write a shopping list and put it in the chat.", "Eggs, milk and bread."},
		{"waiting for something else", stalledImageConversation(), "I'm still waiting for my pizza.", "It should arrive soon."},
		{"replaced request", haiku, "Where is it then?", "Autumn moonlight, a worm digs silently into the chestnut."},
		{"already delivered", delivered, "Where is it then?", "It's right above in the chat."},
	} {
		for _, streaming := range []bool{false, true} {
			turn := runDirectTurn(t, streaming, tc.history, tc.input, tc.reply)
			if turn.err != nil || turn.answer != tc.reply || len(turn.requests) != 1 {
				t.Errorf("%s streaming=%t %q: answer=%q err=%v requests=%d", tc.name, streaming, tc.input, turn.answer, turn.err, len(turn.requests))
			}
		}
	}
}

func TestOwedPictureKeepsExistingChecks(t *testing.T) {
	promised := []Turn{
		{Role: "user", Content: "Show me a picture of a dog."},
		{Role: "assistant", Content: "Let me add it to the chat."},
	}
	reported := []Turn{
		{Role: "user", Content: "Find a picture of a dog and add it to the chat."},
		{Role: "assistant", Content: "I'll work on that and report back."},
		{Role: "assistant", Content: "I found a dog. Let me add it to the chat.", ResponseID: "work-first"},
	}
	for _, tc := range []struct {
		history []Turn
		input   string
		reply   string
		want    error
	}{
		{promised, "Can you share it in this chat?", "Sure thing, dogs are great.", errMissingImageReference},
		{promised, "Post one in this chat.", "Dogs are loyal animals.", errMissingImageReference},
		{reported, "Where is it then?", "I've attached it.", errUnattachedImage},
		{reported, "Where is it then?", "I'll send it right away.", errUnattachedImage},
	} {
		for _, streaming := range []bool{false, true} {
			turn := runDirectTurn(t, streaming, tc.history, tc.input, tc.reply, tc.reply)
			if !errors.Is(turn.err, tc.want) || turn.answer != "" {
				t.Errorf("streaming=%t %q -> %q: answer=%q err=%v", streaming, tc.input, tc.reply, turn.answer, turn.err)
			}
		}
	}
}

func TestDeliveryAnnouncementNeedsTheChatOrAPicture(t *testing.T) {
	o := &OllamaBrain{}
	for _, tc := range []struct {
		request, reply string
		unsupported    bool
	}{
		{"Find a picture of a dog.", "I found one. Let me add it to the chat.", true},
		{"Find a picture of a dog.", "I'm attaching the picture now.", true},
		{"Write a caption for my photo and add it to the chat.", "Adding it here: Sunset over the bay.", false},
		{"Write a caption for my photo and add it to the chat.", "Adding it to the chat: Sunset over the bay.", false},
		{"Update the README to mention the image pipeline.", "Adding the file now.", false},
		{"Update the README to mention the image pipeline.", "I'm adding it now.", false},
		{"Put the meeting on my calendar.", "Let me add it to the chat.", false},
	} {
		if got := o.unsupportedImageResult(tc.reply, tc.request); got != tc.unsupported {
			t.Errorf("%q -> %q: unsupported=%t", tc.request, tc.reply, got)
		}
	}
}
