package brain

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/ollama/ollama/api"
)

// toolModel is a direct-tools persona whose model answers each chat request
// through respond. vision says whether the model can view images.
func toolModel(t *testing.T, vision bool, respond func(w http.ResponseWriter, n int, req api.ChatRequest)) (*OllamaBrain, func() []api.ChatRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []api.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			capabilities := []string{"completion", "tools"}
			if vision {
				capabilities = append(capabilities, "vision")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": capabilities})
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
		respond(w, n, req)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: "You are Obey.", cfg: defaultConversationConfig(), workDir: t.TempDir()}
	return o, func() []api.ChatRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]api.ChatRequest(nil), requests...)
	}
}

func callTool(w http.ResponseWriter, req api.ChatRequest, name string, args map[string]any) {
	answer(w, req, api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{ID: name + "-1", Function: api.ToolCallFunction{Name: name, Arguments: mustArgs(args)}}}}, "stop")
}

func writePNG(t *testing.T, path string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 24, 16))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var directTools = StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect}

// opensThenAnswers calls view_image on path, then answers.
func opensThenAnswers(path string) func(http.ResponseWriter, int, api.ChatRequest) {
	return func(w http.ResponseWriter, n int, req api.ChatRequest) {
		if n == 1 {
			callTool(w, req, "view_image", map[string]any{"path": path})
			return
		}
		answer(w, req, api.Message{Role: "assistant", Content: "A grey rectangle."}, "stop")
	}
}

func toolResult(t *testing.T, req api.ChatRequest, name string) string {
	t.Helper()
	for _, m := range req.Messages {
		if m.Role == "tool" && m.ToolName == name {
			return m.Content
		}
	}
	t.Fatalf("no %s result in the request", name)
	return ""
}

func openedLine(ref chatmedia.Ref) string { return pictureLine(ref, attached, openedByYou) }

func TestViewImageShowsThePictureOnTheNextRound(t *testing.T) {
	for name, ask := range askBothWays {
		t.Run(name, func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			o, sent := toolModel(t, true, opensThenAnswers("shot.png"))
			want := writePNG(t, filepath.Join(o.workDir, "shot.png"))
			if err := ask(t, o, "Look at shot.png.", directTools); err != nil {
				t.Fatal(err)
			}
			requests := sent()
			if len(requests) != 2 || !hasTool(requests[0].Tools, "view_image") {
				t.Fatalf("%d requests; view_image offered: %t", len(requests), len(requests) > 0 && hasTool(requests[0].Tools, "view_image"))
			}
			if len(imagesBy(t, requests[0])) != 0 {
				t.Fatal("the first round already carried a picture")
			}
			question := o.History()[0]
			if len(question.Attachments) != 0 || len(question.OpenedImages) != 1 || question.Content != "Look at shot.png." {
				t.Fatalf("the user's turn now reads %+v", question)
			}
			ref := question.OpenedImages[0]
			carrier := messageWith(t, requests[1], "Look at shot.png.")
			if len(carrier.Images) != 1 || !bytes.Equal(carrier.Images[0], want) || strings.Count(carrier.Content, openedLine(ref)) != 1 {
				t.Fatalf("the next round's user message carries %d images and reads %q", len(carrier.Images), carrier.Content)
			}
			if result := toolResult(t, requests[1], "view_image"); !strings.Contains(result, "Picture "+ref.ID+" ("+ref.Name+") is now attached") {
				t.Fatalf("tool result = %q", result)
			}
			assertWorkingAligned(t, o)
		})
	}
}

// A picture the persona opened is kept on the saved conversation, apart from
// what the user attached, and reaches the model again after a restore.
func TestOpenedPictureSurvivesSaveAndLoad(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, _ := toolModel(t, true, opensThenAnswers("shot.png"))
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	if _, err := o.ThinkFull(t.Context(), "Look at shot.png.", directTools); err != nil {
		t.Fatal(err)
	}
	saved, err := json.Marshal(o.History())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), `"opened_images":[`) {
		t.Fatalf("the saved turns hold no opened picture: %s", saved)
	}
	var turns []Turn
	if err := json.Unmarshal(saved, &turns); err != nil {
		t.Fatal(err)
	}
	if len(turns[0].Attachments) != 0 || len(turns[0].OpenedImages) != 1 {
		t.Fatalf("restored user turn = %+v", turns[0])
	}

	r, sent := toolModel(t, true, func(w http.ResponseWriter, _ int, req api.ChatRequest) {
		answer(w, req, api.Message{Role: "assistant", Content: "Still grey."}, "stop")
	})
	r.LoadHistory(turns)
	if _, err := r.ThinkFull(t.Context(), "What was it again?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if carrier := messageWith(t, sent()[0], "Look at shot.png."); len(carrier.Images) != 1 || !strings.Contains(carrier.Content, openedLine(turns[0].OpenedImages[0])) {
		t.Fatalf("after a restore the turn carries %d images: %q", len(carrier.Images), carrier.Content)
	}
}

func TestViewImageRefusesAFileOutsideTheWorkDirectory(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	outside := filepath.Join(t.TempDir(), "elsewhere.png")
	o, sent := toolModel(t, true, opensThenAnswers(outside))
	writePNG(t, outside)
	if _, err := o.ThinkFull(t.Context(), "Look at that file.", directTools); err != nil {
		t.Fatal(err)
	}
	requests := sent()
	if result := toolResult(t, requests[1], "view_image"); !strings.Contains(result, "must be inside the agent work directory") {
		t.Fatalf("tool result = %q", result)
	}
	if len(imagesBy(t, requests[1])) != 0 || len(o.History()[0].OpenedImages) != 0 {
		t.Fatal("a file outside the work directory was attached")
	}
}

func TestViewImageOnAModelThatCannotViewImages(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, false, opensThenAnswers("shot.png"))
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	reply, err := o.ThinkFull(t.Context(), "Look at shot.png.", directTools)
	if err != nil || reply == "" {
		t.Fatalf("turn: %q %v", reply, err)
	}
	requests := sent()
	if result := toolResult(t, requests[1], "view_image"); !strings.HasSuffix(result, cannotViewImages) {
		t.Fatalf("tool result = %q", result)
	}
	if len(imagesBy(t, requests[1])) != 0 || len(o.History()[0].OpenedImages) != 0 {
		t.Fatal("a model that cannot view images was given the picture")
	}
}

func TestViewImageOnANativeAudioPersona(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, _ := toolModel(t, true, nil)
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	o.SetAudioResolver(noRecordings{})
	o.appendMessage(api.Message{Role: "user", Content: "Look at shot.png."}, nil)
	if result, _ := o.viewImage(t.Context(), "shot.png"); result != cannotViewImages {
		t.Fatalf("tool result = %q", result)
	}
	if len(o.canonical[0].OpenedImages) != 0 {
		t.Fatal("a native-audio persona recorded an opened picture")
	}
}

// An opened picture ages with the message it was opened for.
func TestOpenedPictureAges(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, opensThenAnswers("shot.png"))
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	if _, err := o.ThinkFull(t.Context(), "Look at shot.png.", directTools); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"turn-2", "turn-3", "turn-4"} {
		if _, err := o.ThinkFull(t.Context(), input, StreamOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	ref := o.History()[0].OpenedImages[0]
	aged := pictureLine(ref, agedOut, openedByYou)
	requests := sent()
	if m := messageWith(t, requests[len(requests)-2], "Look at shot.png."); len(m.Images) != 1 {
		t.Fatalf("the third turn's request carries %d images", len(m.Images))
	}
	if m := messageWith(t, requests[len(requests)-1], "Look at shot.png."); len(m.Images) != 0 || strings.Count(m.Content, aged) != 1 {
		t.Fatalf("the fourth turn's request carries %d images: %q", len(m.Images), m.Content)
	}
	assertAgedOnLoad(t, o, "Look at shot.png.", aged)
}

func TestReadFileOnAnImageSaysItIsAnImage(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, func(w http.ResponseWriter, n int, req api.ChatRequest) {
		if n == 1 {
			callTool(w, req, "read_file", map[string]any{"path": "shot.png"})
			return
		}
		answer(w, req, api.Message{Role: "assistant", Content: "It is a picture."}, "stop")
	})
	data := writePNG(t, filepath.Join(o.workDir, "shot.png"))
	if _, err := o.ThinkFull(t.Context(), "Read shot.png.", directTools); err != nil {
		t.Fatal(err)
	}
	result := toolResult(t, sent()[1], "read_file")
	if strings.Contains(result, "PNG\r\n") || !strings.Contains(result, "is an image file (image/png") || !strings.Contains(result, "Use view_image") {
		t.Fatalf("read_file returned %q", result)
	}
	// Where view_image is not offered, read_file names only what the file is.
	plain := toolReadFile(o.workDir, map[string]any{"path": "shot.png"})
	if strings.Contains(plain, "view_image") || !strings.Contains(plain, "image file (image/png, ") || !strings.Contains(plain, " "+strconv.Itoa(len(data))+" bytes") {
		t.Fatalf("read_file without view_image returned %q", plain)
	}
}

// Only the live persona's foreground turn is offered view_image.
func TestViewImageIsOfferedOnlyOnLiveTurns(t *testing.T) {
	o, _ := toolModel(t, true, nil)
	worker := &OllamaBrain{cfg: &config.Config{IsWorker: true}}
	for name, tc := range map[string]struct {
		brain *OllamaBrain
		opts  StreamOptions
		want  bool
	}{
		"live direct":   {o, directTools, true},
		"route only":    {o, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}, false},
		"background":    {o, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, Background: true}, false},
		"worker":        {worker, directTools, false},
		"worker budget": {o, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, WorkerBudget: &WorkerBudget{}}, false},
		"continuation":  {o, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, AgentContinuation: true}, false},
		"discussion":    {o, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, Discussion: &discussion.Directive{}}, false},
		"transient":     {o, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, TransientHistory: true}, false},
	} {
		if got := hasTool(tc.brain.turnToolSession(tc.opts).tools(), "view_image"); got != tc.want {
			t.Errorf("%s: view_image offered = %t", name, got)
		}
	}
}

// Opening a picture after looking around keeps the turn with the persona
// instead of handing it to a worker, which has no view_image.
func TestOpeningAPictureIsNotHandedToAWorker(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, func(w http.ResponseWriter, n int, req api.ChatRequest) {
		switch n {
		case 1:
			callTool(w, req, "list_files", map[string]any{"path": "."})
		case 2:
			callTool(w, req, "view_image", map[string]any{"path": "shot.png"})
		default:
			answer(w, req, api.Message{Role: "assistant", Content: "A grey rectangle."}, "stop")
		}
	})
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	opts := directTools
	opts.BackgroundWorker = true
	turn := runRoutedTurn(t, o, "Find the screenshot and look at it.", opts)
	if turn.result.Err != nil || turn.result.Route != "" {
		t.Fatalf("turn: route=%q err=%v", turn.result.Route, turn.result.Err)
	}
	requests := sent()
	if len(requests) != 3 || len(messageWith(t, requests[2], "Find the screenshot").Images) != 1 {
		t.Fatalf("%d requests; the last does not carry the picture", len(requests))
	}
}

// A picture this request cannot carry is not recorded as opened, and the
// answer says why.
func TestViewImageThatIsNotCarriedIsNotRecorded(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, opensThenAnswers("shot.png"))
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	o.maxImageBytes = 10
	if _, err := o.ThinkFull(t.Context(), "Look at shot.png.", directTools); err != nil {
		t.Fatal(err)
	}
	if result := toolResult(t, sent()[1], "view_image"); !strings.Contains(result, "could not be attached") || !strings.Contains(result, "(10 bytes)") {
		t.Fatalf("tool result = %q", result)
	}
	if carrier := messageWith(t, sent()[1], "Look at shot.png."); len(carrier.Images) != 0 || strings.Contains(carrier.Content, "Picture ") {
		t.Fatalf("the message reads %q with %d images", carrier.Content, len(carrier.Images))
	}
	if len(o.History()[0].OpenedImages) != 0 {
		t.Fatal("a picture that was not attached is recorded as opened")
	}
	if again, _ := o.viewImage(t.Context(), "shot.png"); strings.Contains(again, "already attached") {
		t.Fatalf("a second try reads %q", again)
	}
	o.maxImageBytes = 0
	if _, err := o.ThinkFull(t.Context(), "And now?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if carrier := messageWith(t, sent()[len(sent())-1], "Look at shot.png."); len(carrier.Images) != 0 {
		t.Fatal("a picture that was not attached reached a later request")
	}

	// A model that cannot view images is given that reason.
	blind, blindSent := toolModel(t, false, opensThenAnswers("shot.png"))
	writePNG(t, filepath.Join(blind.workDir, "shot.png"))
	if _, err := blind.ThinkFull(t.Context(), "Look at shot.png.", directTools); err != nil {
		t.Fatal(err)
	}
	if result := toolResult(t, blindSent()[1], "view_image"); !strings.Contains(result, "cannot view images") || strings.Contains(result, "bytes") || strings.Contains(result, "MiB") {
		t.Fatalf("tool result = %q", result)
	}
	if len(blind.History()[0].OpenedImages) != 0 {
		t.Fatal("a picture a model cannot view is recorded as opened")
	}
}

// A file name with a line break is named on one line, once, however many
// requests carry it.
func TestNewlineInAFileNameStaysOnOneLine(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, opensThenAnswers("a\nb.png"))
	writePNG(t, filepath.Join(o.workDir, "a\nb.png"))
	if _, err := o.ThinkFull(t.Context(), "Look at it.", directTools); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := o.ThinkFull(t.Context(), "", StreamOptions{Background: true}); err != nil {
			t.Fatal(err)
		}
	}
	ref := o.History()[0].OpenedImages[0]
	for i, req := range sent()[1:] {
		m := messageWith(t, req, "Look at it.")
		if strings.Count(m.Content, pictureNamed(ref)) != 1 || !strings.Contains(m.Content, ": a b.png, "+openedByYou+".") {
			t.Fatalf("request %d reads %q", i, m.Content)
		}
	}
	assertWorkingAligned(t, o)
}

func callTools(w http.ResponseWriter, req api.ChatRequest, names ...string) {
	var calls []api.ToolCall
	for _, name := range names {
		args := map[string]any{}
		if name == "view_image" {
			args["path"] = "shot.png"
		}
		calls = append(calls, api.ToolCall{ID: name + "-1", Function: api.ToolCallFunction{Name: name, Arguments: mustArgs(args)}})
	}
	answer(w, req, api.Message{Role: "assistant", ToolCalls: calls}, "stop")
}

// After a picture is opened, only an explicit request for background work
// hands the turn off; a look that fails changes nothing.
func TestHandOffAfterALookAtAPicture(t *testing.T) {
	for name, tc := range map[string]struct {
		picture bool
		rounds  [][]string
		route   string
		opened  int
	}{
		"failed look then read_file":       {false, [][]string{{"view_image"}, {"read_file"}}, "spawn", 0},
		"read_file then read_file":         {false, [][]string{{"read_file"}, {"read_file"}}, "spawn", 0},
		"look then read_file":              {true, [][]string{{"view_image"}, {"read_file"}}, "", 1},
		"look then web_search":             {true, [][]string{{"view_image"}, {"web_search"}}, "", 1},
		"look and web_search together":     {true, [][]string{{"view_image", "web_search"}}, "", 1},
		"look then background work":        {true, [][]string{{"view_image"}, {backgroundWorkTool}}, "spawn", 1},
		"look and background work at once": {true, [][]string{{"view_image", backgroundWorkTool}}, "spawn", 0},
	} {
		t.Run(name, func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			o, sent := toolModel(t, true, func(w http.ResponseWriter, n int, req api.ChatRequest) {
				if n <= len(tc.rounds) {
					callTools(w, req, tc.rounds[n-1]...)
					return
				}
				answer(w, req, api.Message{Role: "assistant", Content: "Done."}, "stop")
			})
			if tc.picture {
				writePNG(t, filepath.Join(o.workDir, "shot.png"))
			}
			opts := directTools
			opts.BackgroundWorker = true
			turn := runRoutedTurn(t, o, "Look at shot.png and find out more.", opts)
			if turn.result.Err != nil || turn.result.Route != tc.route {
				t.Fatalf("route=%q err=%v", turn.result.Route, turn.result.Err)
			}
			requests := sent()
			if tc.route == "" {
				final := requests[len(requests)-1]
				for _, round := range tc.rounds {
					for _, tool := range round {
						toolResult(t, final, tool)
					}
				}
			}
			opened := 0
			for _, t := range o.canonical {
				opened += len(t.OpenedImages)
			}
			if opened != tc.opened {
				t.Fatalf("%d pictures recorded as opened, want %d", opened, tc.opened)
			}
		})
	}
}
