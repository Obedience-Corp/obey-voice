package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestAgentAndWorkResultsKeepDesktopImages(t *testing.T) {
	m, _ := mediaTestModel(t)
	ref := m.media.draft[0]
	m.media.draft = nil
	m.handleEvent(events.ResponseReady{Response: "Returned image", Attachments: []chatmedia.Ref{ref}})
	app := newApp(m.cfg, nil, nil)
	app.conversation = m
	desktop := desktopModel{app: app, rt: fakeRuntime()}
	state := desktop.state()
	if len(state.Turns) != 1 || len(state.Turns[0].Attachments) != 1 {
		t.Fatal("agent desktop projection lost image")
	}
	snapshot := stripSnapshot()
	snapshot.Pending[0].Outcome.Attachments = []chatmedia.Ref{ref}
	m.handleEvent(events.WorkStateChanged{Snapshot: snapshot})
	desktop.app.conversation = m
	state = desktop.state()
	count := 0
	for _, refs := range state.WorkResultImages {
		count += len(refs)
	}
	if count != 1 || len(state.WorkResults) != len(state.WorkResultImages) {
		t.Fatal("background desktop images lost alignment")
	}
	m.handleEvent(events.WorkStateChanged{Snapshot: snapshot})
	count = 0
	for _, item := range m.items {
		if item.workResultID != "" {
			count += len(item.attachments)
		}
	}
	if count != 1 {
		t.Fatal("replayed result duplicated image")
	}
}
func TestWorkerInspectorRendersInputAndResultImages(t *testing.T) {
	m, _ := mediaTestModel(t)
	ref := m.media.draft[0]
	result, err := chatmedia.DefaultStore().Import("result.gif", testmedia.GIF())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := chatmedia.DefaultStore().Preview(result)
	if err != nil {
		t.Fatal(err)
	}
	m.media.cache[result.ID] = mediaPreview{preview: preview}
	item := pipeline.WorkerInfo{Thread: []brain.Turn{{Role: "user", Content: "Inspect", Attachments: []chatmedia.Ref{ref}}, {Role: "assistant", Content: "Result", Attachments: []chatmedia.Ref{result}}}}
	item.ID = "worker"
	projection := desktopWorkerThreads([]pipeline.WorkerInfo{item})
	if len(projection["worker"]) != 2 || len(projection["worker"][1].Attachments) != 1 || projection["worker"][1].Attachments[0].Path == "" {
		t.Fatal("native worker thread missing media")
	}
	view := stripANSI(formatWorkerDetail(item, 80, false, m.renderMedia))
	if !strings.Contains(view, "result.gif") || !strings.Contains(view, "▀") {
		t.Fatal("worker did not render media")
	}
}
