package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

const lionRequest = "I want you to show me a picture of a lion in the chat."

// A small local model answered this request in plain text twice.
const plainLion = "Sure! Lions are majestic animals with golden manes."

// pictureServer answers each chat request through respond, which returns an
// HTTP status and the reply. It records every request.
func pictureServer(t *testing.T, respond func(n int, req api.ChatRequest) (int, api.Message)) (*OllamaBrain, func() []api.ChatRequest) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	var mu sync.Mutex
	var requests []api.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools", "vision"}})
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		n := len(requests)
		mu.Unlock()
		status, msg := respond(n, req)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": msg.Content})
			return
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: msg, Done: true, DoneReason: "stop", Metrics: api.Metrics{PromptEvalCount: 10, EvalCount: 5}})
	}))
	t.Cleanup(srv.Close)
	host, _ := url.Parse(srv.URL)
	cfg := &config.Config{AgentName: "Obey", MaxHistory: 20, VoiceToolsEnabled: true, PromptsDir: t.TempDir(), WorkDir: t.TempDir()}
	o := &OllamaBrain{client: api.NewClient(host, srv.Client()), model: "fixture", cfg: cfg, workDir: cfg.WorkDir}
	return o, func() []api.ChatRequest {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(requests)
	}
}

func writeJPEG(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 24, 16)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pictureWorkDir(t *testing.T, o *OllamaBrain) {
	t.Helper()
	writeJPEG(t, filepath.Join(o.workDir, "lion.jpg"))
	writeJPEG(t, filepath.Join(o.workDir, "dolphin.jpg"))
	if err := os.WriteFile(filepath.Join(o.workDir, "notes-diagram.png"), testmedia.PNG(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// choiceReply is the first reply in plain text, then choice as the retry.
func choiceReply(choice string) func(int, api.ChatRequest) (int, api.Message) {
	return func(n int, _ api.ChatRequest) (int, api.Message) {
		if n == 1 {
			return http.StatusOK, api.Message{Role: "assistant", Content: plainLion}
		}
		return http.StatusOK, api.Message{Role: "assistant", Content: choice}
	}
}

var liveDirect = StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect}

func TestPictureRetryAttachesTheChosenFile(t *testing.T) {
	o, sent := pictureServer(t, choiceReply(`{"wanted":"a lion","file":"lion.jpg","caption":"Here is a lion resting in the grass."}`))
	pictureWorkDir(t, o)
	turn := runRoutedTurn(t, o, lionRequest, liveDirect)
	if turn.result.Err != nil {
		t.Fatal(turn.result.Err)
	}
	const want = "Here is a lion resting in the grass.\n![lion.jpg](<lion.jpg>)"
	if turn.answer != want {
		t.Fatalf("answer = %q", turn.answer)
	}
	history := o.History()
	if reply := history[len(history)-1]; reply.Role != "assistant" || reply.Content != want {
		t.Fatalf("recorded reply = %+v", reply)
	}
	requests := sent()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	retry := requests[1]
	if len(retry.Tools) != 0 || thinkEnabled(retry.Think) || retry.Stream == nil || *retry.Stream {
		t.Fatalf("the choice was sent with tools=%d think=%v stream=%v", len(retry.Tools), retry.Think, retry.Stream)
	}
	format := string(retry.Format)
	if !(strings.Index(format, `"wanted"`) < strings.Index(format, `"file"`) && strings.Index(format, `"file"`) < strings.Index(format, `"caption"`)) {
		t.Fatalf("the schema does not ask for wanted, file, caption in order: %s", format)
	}
	var schema struct {
		Properties struct {
			File struct {
				Enum []string `json:"enum"`
			} `json:"file"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(retry.Format, &schema); err != nil {
		t.Fatal(err)
	}
	enum := schema.Properties.File.Enum
	if len(enum) != 4 || enum[0] != "none" || !slices.Contains(enum, "lion.jpg") || !slices.Contains(enum, "dolphin.jpg") || !slices.Contains(enum, "notes-diagram.png") || len(schema.Required) != 3 {
		t.Fatalf("schema = %s", format)
	}
	instruction := retry.Messages[len(retry.Messages)-1].Content
	if !strings.HasPrefix(instruction, lionRequest) || !strings.Contains(instruction, "First write in `wanted`") || !strings.Contains(instruction, `"lion.jpg"`) {
		t.Fatalf("the choice instruction is not on the request: %q", instruction)
	}
	// The reply goes through the importer a model-written reference uses.
	cleaned, refs, err := chatmedia.DefaultStore().ImportReply(turn.answer, o.workDir)
	if err != nil || len(refs) != 1 || refs[0].Name != "lion.jpg" || cleaned != "Here is a lion resting in the grass.\nlion.jpg" {
		t.Fatalf("import: %q %+v %v", cleaned, refs, err)
	}
}

func TestPictureRetryWithNoFittingFileSaysSo(t *testing.T) {
	for _, tc := range []struct{ caption, want string }{
		{"I could not find a picture of a giraffe here.", "I could not find a picture of a giraffe here."},
		// A caption that would pass for a delivery is not said.
		{"Here is a giraffe for you.", noPictureLine},
		{"I couldn't find one, but I'll fetch one online.", noPictureLine},
		{"", noPictureLine},
	} {
		choice, _ := json.Marshal(map[string]string{"wanted": "a giraffe", "file": "none", "caption": tc.caption})
		o, sent := pictureServer(t, choiceReply(string(choice)))
		pictureWorkDir(t, o)
		turn := runRoutedTurn(t, o, "Show me a picture of a giraffe in the chat.", liveDirect)
		if turn.result.Err != nil || turn.answer != tc.want || len(sent()) != 2 {
			t.Fatalf("caption %q: answer=%q err=%v requests=%d", tc.caption, turn.answer, turn.result.Err, len(sent()))
		}
		history := o.History()
		if reply := history[len(history)-1]; reply.Content != tc.want || len(reply.Attachments) != 0 {
			t.Fatalf("recorded reply = %+v", reply)
		}
	}
}

// A choice the host cannot use, or a request that fails, ends the turn with
// the spoken no-picture line and the visible error, never in silence.
func TestFailedPictureRetrySaysNoPictureWasAttached(t *testing.T) {
	for name, respond := range map[string]func(int, api.ChatRequest) (int, api.Message){
		"garbage":       choiceReply("Here you go, a lion!"),
		"not offered":   choiceReply(`{"wanted":"a lion","file":"lion.png","caption":"Here is a lion."}`),
		"missing field": choiceReply(`{"wanted":"a lion","caption":"Here is a lion."}`),
		"tool call": func(n int, _ api.ChatRequest) (int, api.Message) {
			if n == 1 {
				return http.StatusOK, api.Message{Role: "assistant", Content: plainLion}
			}
			return http.StatusOK, api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "view_image", Arguments: mustArgs(map[string]any{"path": "lion.jpg"})}}}}
		},
		"format rejected": func(n int, req api.ChatRequest) (int, api.Message) {
			if len(req.Format) > 0 {
				return http.StatusBadRequest, api.Message{Content: "format is not supported"}
			}
			return http.StatusOK, api.Message{Role: "assistant", Content: plainLion}
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, sent := pictureServer(t, respond)
			pictureWorkDir(t, o)
			turn := runRoutedTurn(t, o, lionRequest, liveDirect)
			err := turn.result.Err
			if turn.answer != noPictureLine || err == nil || !turn.result.Recovered || !errors.Is(err, errPictureChoice) || !strings.Contains(err.Error(), "no image was attached") {
				t.Fatalf("answer=%q err=%v recovered=%t", turn.answer, err, turn.result.Recovered)
			}
			if len(sent()) != 2 {
				t.Fatalf("requests = %d", len(sent()))
			}
			history := o.History()
			if reply := history[len(history)-1]; reply.Content != noPictureLine {
				t.Fatalf("recorded reply = %+v", reply)
			}
			// The picture is still owed.
			if _, owed := FindOwedImage(history); !owed {
				t.Fatal("the picture is no longer owed after the no-picture line")
			}
		})
	}
}

// A turn stopped during the choice is not answered with the no-picture line.
func TestStoppedPictureRetryIsNotAnswered(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o, _ := pictureServer(t, func(n int, _ api.ChatRequest) (int, api.Message) {
		if n == 1 {
			return http.StatusOK, api.Message{Role: "assistant", Content: plainLion}
		}
		cancel()
		return http.StatusOK, api.Message{Role: "assistant", Content: `{"wanted":"a lion","file":"lion.jpg","caption":"Here."}`}
	})
	pictureWorkDir(t, o)
	stream, err := o.ThinkStream(ctx, lionRequest, liveDirect)
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := drainStream(t, stream)
	result := <-stream.Done
	if answer != "" || !errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, errPictureChoice) {
		t.Fatalf("a stopped turn was answered: %q %v", answer, result.Err)
	}
}

// Without a usable file the retry is the free-form correction with tools, as
// before, and an owed follow-up keeps its own correction.
func TestPictureRetryNeedsCandidatesAndALiveRequest(t *testing.T) {
	o, sent := pictureServer(t, choiceReply("Here's the lion.\n![lion](<lion.jpg>)"))
	turn := runRoutedTurn(t, o, lionRequest, liveDirect)
	retry := sent()[1]
	if len(retry.Format) != 0 || len(retry.Tools) == 0 || !strings.Contains(retry.Messages[len(retry.Messages)-1].Content, "Local filenames that the host successfully decoded (JSON): []") {
		t.Fatalf("retry without candidates: format=%s tools=%d", retry.Format, len(retry.Tools))
	}
	if turn.answer != noPictureLine || turn.result.Err == nil {
		t.Fatalf("answer=%q err=%v", turn.answer, turn.result.Err)
	}

	o, _ = pictureServer(t, nil)
	pictureWorkDir(t, o)
	for name, tc := range map[string]struct {
		input  string
		direct imageRequestContext
		opts   StreamOptions
		want   bool
	}{
		"live request":        {lionRequest, imageMentioned, liveDirect, true},
		"owed follow-up":      {"Where is it then?", owedImageFollowup, liveDirect, false},
		"not asking to see":   {"Do you like pictures of lions?", imageMentioned, liveDirect, false},
		"worker":              {lionRequest, imageMentioned, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, WorkerBudget: &WorkerBudget{}}, false},
		"background report":   {lionRequest, imageMentioned, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, Background: true}, false},
		"route only":          {lionRequest, imageMentioned, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}, false},
		"no picture involved": {lionRequest, noImageContext, liveDirect, false},
	} {
		if got := o.choosePicture(t.Context(), tc.input, tc.direct, tc.opts) != nil; got != tc.want {
			t.Errorf("%s: picture choice = %t", name, got)
		}
	}
}

// The choice is made at temperature 0 and the host lines are fixed, so asking
// again can produce the same reply word for word. It is still delivered.
func TestPictureRetryAnswersTheSameRequestTwice(t *testing.T) {
	for name, choice := range map[string]string{
		"chosen": `{"wanted":"a lion","file":"lion.jpg","caption":"Here is a lion."}`,
		"none":   `{"wanted":"a lion","file":"none","caption":"Here it is."}`,
		"failed": "Here is a lion!",
	} {
		t.Run(name, func(t *testing.T) {
			o, sent := pictureServer(t, func(_ int, req api.ChatRequest) (int, api.Message) {
				if len(req.Format) == 0 {
					return http.StatusOK, api.Message{Role: "assistant", Content: plainLion}
				}
				return http.StatusOK, api.Message{Role: "assistant", Content: choice}
			})
			pictureWorkDir(t, o)
			var answers []string
			for range 2 {
				turn := runRoutedTurn(t, o, lionRequest, liveDirect)
				if name == "failed" {
					if !errors.Is(turn.result.Err, ErrNoPictureAttached) {
						t.Fatalf("err = %v", turn.result.Err)
					}
				} else if turn.result.Err != nil {
					t.Fatalf("err = %v", turn.result.Err)
				}
				answers = append(answers, turn.answer)
			}
			if answers[0] == "" || answers[1] != answers[0] || len(sent()) != 4 {
				t.Fatalf("answers=%q requests=%d", answers, len(sent()))
			}
		})
	}
}

// Pictures kept in a subfolder are offered by their relative path, and the
// chosen one is attached.
func TestPictureRetryChoosesAFileInASubfolder(t *testing.T) {
	o, sent := pictureServer(t, choiceReply(`{"wanted":"a lion","file":"images/lion.jpg","caption":"Here is a lion."}`))
	if err := os.MkdirAll(filepath.Join(o.workDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, filepath.Join(o.workDir, "images", "dolphin.jpg"))
	writeJPEG(t, filepath.Join(o.workDir, "images", "lion.jpg"))
	turn := runRoutedTurn(t, o, lionRequest, liveDirect)
	const want = "Here is a lion.\n![lion.jpg](<images/lion.jpg>)"
	if turn.result.Err != nil || turn.answer != want {
		t.Fatalf("answer=%q err=%v", turn.answer, turn.result.Err)
	}
	if format := string(sent()[1].Format); !strings.Contains(format, `"enum":["none","images/dolphin.jpg","images/lion.jpg"]`) {
		t.Fatalf("schema = %s", format)
	}
	_, refs, err := chatmedia.DefaultStore().ImportReply(turn.answer, o.workDir)
	if err != nil || len(refs) != 1 || refs[0].Name != "lion.jpg" {
		t.Fatalf("import: %+v %v", refs, err)
	}
}

// Names are listed without decoding them; a chosen file that is not a usable
// picture fails the reply check and the persona says no picture was attached.
func TestPictureRetryChoosingAnUnusableFileSaysNoPictureWasAttached(t *testing.T) {
	o, _ := pictureServer(t, choiceReply(`{"wanted":"a lion","file":"images/lion.jpg","caption":"Here is a lion."}`))
	if err := os.MkdirAll(filepath.Join(o.workDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.workDir, "images", "lion.jpg"), []byte("<html>not found</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	turn := runRoutedTurn(t, o, lionRequest, liveDirect)
	if turn.answer != noPictureLine || !errors.Is(turn.result.Err, ErrNoPictureAttached) || !turn.result.Recovered {
		t.Fatalf("answer=%q err=%v", turn.answer, turn.result.Err)
	}
	for _, saved := range o.History() {
		if strings.Contains(saved.Content, "Here is a lion.") {
			t.Fatalf("the rejected reply was recorded: %+v", saved)
		}
	}
}

// A choice cut off after its file, as a small model does when it fills its
// output budget with whitespace, still counts; the host writes the caption.
func TestPictureRetryKeepsAChoiceCutOffAfterItsFile(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		{`{ "wanted": "a giraffe" , "file": "none"` + strings.Repeat(" ", 200), noPictureLine},
		{`{"wanted":"a lion","file":"lion.jpg",` + strings.Repeat("\t", 200), pictureCaption + "\n![lion.jpg](<lion.jpg>)"},
	} {
		o, _ := pictureServer(t, choiceReply(tc.content))
		pictureWorkDir(t, o)
		turn := runRoutedTurn(t, o, lionRequest, liveDirect)
		if turn.result.Err != nil || turn.answer != tc.want {
			t.Fatalf("%q: answer=%q err=%v", tc.content[:40], turn.answer, turn.result.Err)
		}
	}
	for _, cut := range []string{`{"wanted":"a lion","fi`, `{"wanted":"a lion","file":"lio`, `{"file":"lion.jpg"`} {
		if _, _, ok := truncatedPictureChoice(cut); ok {
			t.Errorf("%q read as a choice", cut)
		}
	}
}

func TestPictureChoiceSchemaQuotesFileNamesAndAsksForTheSubjectFirst(t *testing.T) {
	c := &pictureChoice{files: []string{`say "cheese".png`, `images/a\b.jpg`}}
	raw := c.format()
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v\n%s", err, raw)
	}
	if got := schema.Properties["file"].Enum; !slices.Equal(got, []string{"none", `say "cheese".png`, `images/a\b.jpg`}) {
		t.Fatalf("file enum = %q", got)
	}
	wanted, file, caption := bytes.Index(raw, []byte(`"wanted"`)), bytes.Index(raw, []byte(`"file"`)), bytes.Index(raw, []byte(`"caption"`))
	if !(wanted < file && file < caption) {
		t.Fatalf("properties out of order: %s", raw)
	}
}
