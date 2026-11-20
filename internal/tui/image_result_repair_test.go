package tui

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ollama/ollama/api"
)

// Exercise the actual router, bounded correction, worker admission, file import,
// event delivery and Bubble Tea loop. Only model responses are fixtures.
func TestImageClaimRepairReachesTerminal(t *testing.T) {
	runImageClaimTerminal(t, "spawn")
}

func TestPostChatImageClaimShowsTerminalError(t *testing.T) {
	runImageClaimTerminal(t, "chat")
}

func TestDirectImageRepairReachesTerminal(t *testing.T) {
	t.Setenv("OBEY_VOICE_TUI_IMAGES", "blocks")
	runImageClaimTerminal(t, "direct")
}

func runImageClaimTerminal(t *testing.T, mode string) {
	postChat := mode == "chat"
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	for name, data := range map[string][]byte{"result.png": testmedia.PNG(), "result.gif": testmedia.GIF()} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "direct" {
		oversize := testmedia.GIF()
		binary.LittleEndian.PutUint16(oversize[6:8], 2048)
		binary.LittleEndian.PutUint16(oversize[8:10], 2048)
		if err := os.WriteFile(filepath.Join(root, "oversize.gif"), oversize, 0600); err != nil {
			t.Fatal(err)
		}
	}
	const claim = "I’ve attached them below."
	var liveCalls, workerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "live"}, {"name": "worker"}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools"}})
		case "/api/chat":
			var req api.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			msg := api.Message{Role: "assistant"}
			if req.Model == "live" {
				first := liveCalls.Add(1) == 1
				if mode == "direct" {
					if first {
						msg.Content = "Here's an image from the workspace.\n![tui](oversize.gif)"
					} else {
						repair := req.Messages[len(req.Messages)-1].Content
						if !strings.Contains(repair, "16 megapixels") || !strings.Contains(repair, "oversize.gif") || !strings.Contains(repair, "result.png") || len(req.Tools) == 0 {
							t.Error("direct repair omitted the import error or the tools")
						}
						msg.Content = "Here are the pictures from the docs:\n![Still](result.png)\n![Animation](result.gif)"
					}
				} else if first != postChat {
					msg.Content = claim
				} else {
					var args api.ToolCallFunctionArguments
					kind := "spawn"
					if postChat {
						kind = "chat"
					}
					_ = json.Unmarshal([]byte(`{"kind":"`+kind+`"}`), &args)
					msg.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: args}}}
				}
			} else {
				workerCalls.Add(1)
				msg.Content = "Here are the files.\n![PNG result](<" + filepath.Join(root, "result.png") + ">)\n![GIF result](<" + filepath.Join(root, "result.gif") + ">)"
			}
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: msg, Done: true})
		default:
			t.Errorf("unexpected model endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "live", VoiceWorkerProvider: "ollama", VoiceToolsEnabled: true, AgentName: "Obey", MaxHistory: 20, WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), SkillsDir: filepath.Join(root, "skills")}
	live, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	workerCfg := *cfg
	workerCfg.OllamaModel = "worker"
	worker, err := brain.NewOllama(&workerCfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	bus := events.NewBus()
	p := &pipeline.Pipeline{Brain: live, Events: bus, VoiceToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly}
	if mode == "direct" {
		p.RouterToolsPolicy = brain.RouterToolsDirect
		cfg.ContinuityIntegrityEnabled = true
	}
	jobs := job.NewTable(ctx)
	defer jobs.Close()
	pipeline.NewAgency(p, jobs, worker, cfg, nil).Wire()
	m := sizedConversation(t, 100, 42)
	m.cfg = cfg
	initial := m.startConversation(conversationDeps{runner: p, ctx: ctx, bus: bus})
	m.input.SetValue("Find a PNG and GIF and show them in this chat.")
	h := &imageRepairTerminal{model: m, initial: initial, stopOnError: postChat, wantImages: 2, stopOnTurnEnd: mode == "direct"}
	var output bytes.Buffer
	if _, err := tea.NewProgram(h, tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithInput(nil), tea.WithOutput(&output)).Run(); err != nil {
		t.Fatalf("terminal did not receive the result: %v\n%s", err, stripANSI(output.String()))
	}
	wantWorkers := int32(1)
	if postChat || mode == "direct" {
		wantWorkers = 0
	}
	if liveCalls.Load() != 2 || workerCalls.Load() != wantWorkers {
		t.Fatalf("model requests: live=%d worker=%d", liveCalls.Load(), workerCalls.Load())
	}
	h.model.viewport.GotoBottom()
	view := stripANSI(h.model.viewport.View())
	want := []string{"result.png", "result.gif"}
	if postChat {
		want = []string{"no image was attached"}
	}
	for _, name := range want {
		if !strings.Contains(view, name) {
			t.Fatalf("missing %s preview: %s", name, view)
		}
	}
	if mode == "direct" {
		if !strings.Contains(view, "▀") {
			t.Fatalf("reply has no rendered image pixels: %s", view)
		}
		history := live.History()
		reply := history[len(history)-1]
		if len(reply.Attachments) != 2 || strings.Contains(reply.Content, "![") || strings.Contains(view, "![") || strings.Contains(view, "oversize.gif") {
			t.Fatalf("direct reply did not retain the verified images: %+v\n%s", reply, view)
		}
		if data, err := chatmedia.DefaultStore().Read(reply.Attachments[0]); err != nil || !bytes.Equal(data, testmedia.PNG()) {
			t.Fatalf("stored image differs from the delivered PNG: %v", err)
		}
	}
	if strings.Contains(stripANSI(output.String()), claim) {
		t.Fatal("unsupported attachment claim reached the terminal")
	}
}

type imageRepairProbe struct{}
type imageRepairTerminal struct {
	model         conversationModel
	initial       tea.Cmd
	stopOnError   bool
	wantImages    int
	stopOnTurnEnd bool
}

func (h *imageRepairTerminal) Init() tea.Cmd {
	return tea.Batch(h.initial, tea.Tick(10*time.Millisecond, func(time.Time) tea.Msg { return tea.KeyMsg{Type: tea.KeyEnter} }), h.probe())
}
func (h *imageRepairTerminal) probe() tea.Cmd {
	return tea.Tick(10*time.Millisecond, func(time.Time) tea.Msg { return imageRepairProbe{} })
}
func (h *imageRepairTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(imageRepairProbe); ok {
		if h.stopOnError && strings.Contains(stripANSI(h.model.View()), "no image was attached") {
			return h, tea.Quit
		}
		for _, item := range h.model.items {
			if len(item.attachments) == h.wantImages {
				for _, ref := range item.attachments {
					preview := h.model.media.cache[ref.ID]
					if len(preview.preview.Frames) == 0 && preview.err == nil {
						return h, h.probe()
					}
				}
				return h, tea.Quit
			}
		}
		if h.stopOnTurnEnd && h.model.lastMetrics.Outcome != "" {
			return h, tea.Quit
		}
		return h, h.probe()
	}
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	return h, cmd
}
func (h *imageRepairTerminal) View() string { return h.model.View() }
