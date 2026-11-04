package pipeline

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

func TestSpeechProjectionRejectsRawFunctionMarkup(t *testing.T) {
	fixture := "<function=read_file>\n<parameter=path>\n/workspace/campaigns/example\n</parameter>\n</function>\n</tool_call>"
	for split := 0; split <= len(fixture); split++ {
		p := NewSpeechProjection()
		first, second, tail := p.Speak(fixture[:split]), p.Speak(fixture[split:]), p.Flush()
		if first.Heard() || second.Heard() || tail.Heard() {
			t.Fatalf("split %d leaked speech: %+v %+v %+v", split, first, second, tail)
		}
		if first.Filtered+second.Filtered+tail.Filtered != "" {
			t.Fatalf("split %d leaked reply text", split)
		}
		if !errors.Is(p.Err(), textclean.ErrRawToolMarkup) {
			t.Fatalf("split %d failed invisibly: %v", split, p.Err())
		}
	}
}

func TestSpeechProjectionReportsTruncatedToolTagAtEOF(t *testing.T) {
	p := NewSpeechProjection()
	if got := p.Speak("<tool_call"); got.Heard() {
		t.Fatalf("partial tool tag spoken: %+v", got)
	}
	tail := p.Flush()
	if tail.Heard() || !errors.Is(tail.Err, textclean.ErrRawToolMarkup) {
		t.Fatalf("truncated tool tag not reported: %+v", tail)
	}
}

func TestSpeechProjectionPreservesComparisonAndFencedExample(t *testing.T) {
	p := NewSpeechProjection()
	heard := speakAll(p, "Three < five.", "```xml", "<function=read_file>", "</function>", "```", "That was a code example.")
	if p.Err() != nil || strings.Join(heard, " ") != "Three < five. That was a code example." {
		t.Fatalf("ordinary prose/code changed: heard %q err=%v", heard, p.Err())
	}
}

func TestStreamResponseReportsRawToolMarkupWithoutReplyText(t *testing.T) {
	bus := events.NewBus()
	p := &Pipeline{Events: bus}
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	errorsSeen := 0
	events.Subscribe(bus, func(event events.Error) {
		if event.Stage == "brain" && strings.Contains(event.Message, "raw tool-call markup") {
			errorsSeen++
		}
	})
	chunks := make(chan string, 3)
	chunks <- "<fun"
	chunks <- "ction=read_file>\n<parameter=path>\n/workspace/campaigns/example"
	chunks <- "\n</parameter>\n</function>\n</tool_call>"
	close(chunks)
	done := make(chan brain.StreamResult)
	close(done)
	reply, interrupted, err := p.streamResponse(context.Background(), nil, &brain.Stream{Chunks: chunks, Done: done}, false, metrics, turn, nil)
	if err != nil || interrupted || reply != "" || errorsSeen != 1 || !metrics.degraded {
		t.Fatalf("reply=%q interrupted=%v err=%v errors=%d degraded=%v", reply, interrupted, err, errorsSeen, metrics.degraded)
	}
}
