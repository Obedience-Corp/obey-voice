package brain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// speakerCase is one live speaker label and how a non-group prompt attributes
// it. line is "" when the turn must read as having no speaker.
type speakerCase struct {
	name, label, line string
}

var speakerCases = []speakerCase{
	{"none", "", ""},
	{"unidentified", "unknown", ""},
	{"unidentified capitalized", "Unknown", ""},
	{"unidentified padded", " unknown ", ""},
	{"enrolled", "Lance", "Lance"},
	{"renamed diarization", "speaker-1", "Lance"},
	{"anonymous diarization", "speaker-2", "speaker-2"},
}

var speakerCaseNames = testNames{"speaker-1": "Lance"}

func TestPromptSpeakerDropsOnlyUnidentified(t *testing.T) {
	for _, tc := range speakerCases {
		got := PromptSpeaker(tc.label)
		want := strings.TrimSpace(tc.label)
		if tc.line == "" {
			want = ""
		}
		if got != want {
			t.Errorf("%s: PromptSpeaker(%q) = %q, want %q", tc.name, tc.label, got, want)
		}
	}
}

func userContents(messages []api.Message) []string {
	var out []string
	for _, m := range messages {
		if m.Role == "user" {
			out = append(out, m.Content)
		}
	}
	return out
}

func wantUserLine(tc speakerCase, text string) string {
	if tc.line == "" {
		return text
	}
	return tc.line + ": " + text
}

func TestOllamaLiveAndRestoredUserMessagesNameOnlyIdentifiedSpeakers(t *testing.T) {
	for _, tc := range speakerCases {
		t.Run(tc.name, func(t *testing.T) {
			o := &OllamaBrain{cfg: &config.Config{}}
			o.SetSpeakerNames(speakerCaseNames)
			o.LoadHistory([]Turn{{Role: "user", Speaker: tc.label, Content: "I'm doing good."}, {Role: "assistant", Content: "Glad to hear it."}})
			if err := o.appendUserInput("What's next?", StreamOptions{Speaker: tc.label}); err != nil {
				t.Fatal(err)
			}
			got := userContents(o.buildMessages(""))
			if len(got) != 2 {
				t.Fatalf("user messages = %q", got)
			}
			if want := wantUserLine(tc, "I'm doing good."); got[0] != want {
				t.Errorf("restored user = %q, want %q", got[0], want)
			}
			if want := wantUserLine(tc, "What's next?"); !strings.HasPrefix(got[1], want) {
				t.Errorf("live user = %q, want prefix %q", got[1], want)
			}
			for _, turn := range o.History() {
				if turn.Role == "user" && strings.TrimSpace(turn.Speaker) != strings.TrimSpace(tc.label) {
					t.Errorf("stored speaker = %q, want %q kept for the session file", turn.Speaker, tc.label)
				}
			}
		})
	}
}

func TestOllamaGroupModeTreatsUnidentifiedAsNoSpeaker(t *testing.T) {
	for _, tc := range []struct{ label, header string }{
		{"", "Unknown user:\n"},
		{"unknown", "Unknown user:\n"},
		{"speaker-1", "Lance user:\n"},
		{"speaker-2", "Unknown user:\n"},
	} {
		o := &OllamaBrain{cfg: &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}}
		o.SetGroupParticipants([]GroupParticipant{{Name: "Samantha"}, {Name: "Alex"}})
		o.SetSpeakerNames(speakerCaseNames)
		o.LoadHistory([]Turn{{Role: "user", Speaker: tc.label, Content: "anyone there?"}})
		got := userContents(o.buildMessages(""))
		if len(got) != 1 || !strings.HasPrefix(got[0], tc.header+"anyone there?") {
			t.Errorf("label %q: group user = %q, want header %q", tc.label, got, tc.header)
		}
		if strings.Contains(got[0], "unknown") {
			t.Errorf("label %q: group user names the unidentified label: %q", tc.label, got[0])
		}
	}
}

func TestOllamaNativeCompletionNamesOnlyIdentifiedSpeakers(t *testing.T) {
	for _, tc := range speakerCases {
		o := &OllamaBrain{cfg: &config.Config{}}
		o.SetSpeakerNames(speakerCaseNames)
		messages, err := o.nativeCompletionContext(t.Context(), []Turn{{Role: "user", Speaker: tc.label, Content: "Is it done?"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := userContents(messages); len(got) != 1 || got[0] != wantUserLine(tc, "Is it done?") {
			t.Errorf("%s: native completion user = %q", tc.name, got)
		}
	}
}

func TestCompletionPromptNamesOnlyIdentifiedSpeakers(t *testing.T) {
	for _, tc := range []struct{ label, line string }{
		{"", "Current user input: Is it done?\n"},
		{"unknown", "Current user input: Is it done?\n"},
		{"Lance", "Current user input (Lance): Is it done?\n"},
		{"speaker-2", "Current user input (speaker-2): Is it done?\n"},
	} {
		prompt := completionPrompt(BuildCompletionRequest("Is it done?", tc.label, nil, nil))
		if !strings.Contains(prompt, tc.line) {
			t.Errorf("label %q: prompt lacks %q:\n%s", tc.label, tc.line, prompt)
		}
	}
}

func TestFlattenedAndCodexPromptsNameOnlyIdentifiedSpeakers(t *testing.T) {
	for _, tc := range speakerCases {
		label := tc.line
		if label == "" {
			label = "User"
		}
		turn := Turn{Role: "user", Speaker: tc.label, Content: "hi"}
		if got := promptUserLine(turn, "Obey", speakerCaseNames); got != label+": hi" {
			t.Errorf("%s: flattened line = %q, want %q", tc.name, got, label+": hi")
		}
		var record struct {
			Speaker string `json:"speaker"`
		}
		if err := json.Unmarshal([]byte(cliHistoryRecord(turn, speakerCaseNames)), &record); err != nil {
			t.Fatal(err)
		}
		if record.Speaker != label {
			t.Errorf("%s: codex speaker = %q, want %q", tc.name, record.Speaker, label)
		}
	}
}

func TestArchiveNotesWriteTheAttributedLine(t *testing.T) {
	for _, tc := range speakerCases {
		o := &OllamaBrain{cfg: continuityConfig()}
		o.SetSpeakerNames(speakerCaseNames)
		o.history = []api.Message{{Role: "user", Content: encodeOllamaUser(tc.label, "I'm doing good.")}, {Role: "assistant", Content: "Glad to hear it."}}
		o.archiveMessages(o.history)
		archive := o.ContinuityArchive()
		if len(archive) != 1 {
			t.Fatalf("archive = %+v", archive)
		}
		want := "user: " + wantUserLine(tc, "I'm doing good.") + "\nassistant: Glad to hear it.\n"
		if archive[0].Summary != want {
			t.Errorf("%s: note = %q, want %q", tc.name, archive[0].Summary, want)
		}
	}
}
