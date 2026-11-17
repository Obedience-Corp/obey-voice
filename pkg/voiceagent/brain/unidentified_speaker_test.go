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

// groupHeaderCases: label, rename table, configured user name, and the header
// a group-mode user message must carry.
var groupHeaderCases = []struct {
	name, label string
	names       SpeakerNames
	configured  string
	header      string
}{
	{"none, configured", "", nil, "Lance", "Lance user:\n"},
	{"none, unconfigured", "", nil, "", "User:\n"},
	{"unidentified, configured", "unknown", testNames{"speaker-1": "Ana"}, "Lance", "Lance user:\n"},
	{"unidentified, unconfigured", "Unknown", nil, "", "User:\n"},
	{"enrolled with renames", "Maria", testNames{"speaker-1": "Ana"}, "Lance", "Maria user:\n"},
	{"enrolled without renames", "Maria", nil, "", "Maria user:\n"},
	{"renamed diarization", "speaker-1", testNames{"speaker-1": "Ana"}, "Lance", "Ana user:\n"},
	{"unrenamed diarization", "speaker-2", testNames{"speaker-1": "Ana"}, "Lance", "Unknown user:\n"},
	{"diarization without renames", "speaker-1", nil, "Lance", "Unknown user:\n"},
}

func TestOllamaGroupHeaderNamesTheHuman(t *testing.T) {
	for _, tc := range groupHeaderCases {
		t.Run(tc.name, func(t *testing.T) {
			o := &OllamaBrain{cfg: &config.Config{ActivePersona: "samantha", AgentName: "Samantha", UserDisplayName: tc.configured}}
			o.SetGroupParticipants([]GroupParticipant{{Name: "Samantha"}, {Name: "Alex"}})
			o.SetSpeakerNames(tc.names)
			o.LoadHistory([]Turn{{Role: "user", Speaker: tc.label, Content: "anyone there?"}})
			got := userContents(o.buildMessages(""))
			if len(got) != 1 || !strings.HasPrefix(got[0], tc.header+"anyone there?") {
				t.Errorf("group user = %q, want header %q", got, tc.header)
			}
		})
	}
}

// A stored turn's header comes from its own speaker, so later turns with other
// live labels never rewrite it and the earlier prompt bytes stay put.
func TestOllamaGroupHeaderIsStableAcrossTurns(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{ActivePersona: "samantha", AgentName: "Samantha", UserDisplayName: "Lance"}}
	o.SetGroupParticipants([]GroupParticipant{{Name: "Samantha"}, {Name: "Alex"}})
	o.LoadHistory([]Turn{{Role: "user", Speaker: "unknown", Content: "first"}, {Role: "assistant", PersonaID: "samantha", PersonaDisplayName: "Samantha", Content: "ok"}})
	before := o.buildMessages("")
	if err := o.appendUserInput("second", StreamOptions{Speaker: "Maria"}); err != nil {
		t.Fatal(err)
	}
	after := o.buildMessages("")
	if !strings.HasPrefix(before[1].Content, "Lance user:\nfirst") || after[1].Content != "Lance user:\nfirst" {
		t.Fatalf("earlier header changed: before %q after %q", before[1].Content, after[1].Content)
	}
	if last := userContents(after); !strings.HasPrefix(last[len(last)-1], "Maria user:\nsecond") {
		t.Fatalf("new header = %q", last[len(last)-1])
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

func TestCompletionPromptNamesTheHuman(t *testing.T) {
	for _, tc := range groupHeaderCases {
		o := &OllamaBrain{cfg: &config.Config{UserDisplayName: tc.configured}}
		o.SetSpeakerNames(tc.names)
		prompt := completionPrompt(BuildCompletionRequest("Is it done?", tc.label, nil, nil), o.humanName(tc.label))
		name := strings.TrimSuffix(strings.TrimSuffix(tc.header, ":\n"), " user")
		if want := "Current user input (" + name + "): Is it done?\n"; !strings.Contains(prompt, want) {
			t.Errorf("%s: prompt lacks %q:\n%s", tc.name, want, prompt)
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
		o.archiveTurns(o.history, nil)
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
