package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// An image is shown early only when the turn will not route the reply to a
// worker, so the routed acknowledgement is never replaced by the image reply.
func TestEarlyReplyImageAgreesWithPromiseRepair(t *testing.T) {
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	dir := chatmedia.DefaultStore().Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(dir, "chart.png")
	if err := os.WriteFile(image, testmedia.PNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	directive := "\n![Chart](<" + image + ">)"
	lead := strings.Repeat("The chart has a long title on the axis. ", 8)
	for _, tc := range []struct {
		name, reply   string
		routed, early bool
	}{
		// An inline directive is an image reply. Its path keeps the
		// sentence from reading as a promise, so the turn does not route.
		{name: "directive inside the promise", reply: "I'll use the ![c](<" + image + ">) plotting tool", early: true},
		{name: "vocal tag inside the promise", reply: lead + "I'll [laughs] use the plotting tool." + directive, routed: true},
		{name: "plain promise", reply: lead + "I'll use the plotting tool." + directive, routed: true},
		{name: "no promise", reply: lead + "Here is the chart." + directive, early: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			player := newFakePlayer(100 * time.Millisecond)
			defer player.Close()
			p := &Pipeline{Brain: &imageReplyBrain{fakeBrain{chunks: []string{tc.reply}}}, TTS: &progressiveFakeTTS{}, Player: player, Events: events.NewBus(), VoiceFailedRouteRepair: true}
			shown := make(chan events.ReplyImagesReady, 1)
			events.Subscribe(p.Events, func(e events.ReplyImagesReady) { shown <- e })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			stream, err := p.Brain.ThinkStream(ctx, "plot it", brain.StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			repair := &promiseRepairState{enabled: true}
			metrics := newTurnMetrics()
			if _, _, err := p.streamResponse(ctx, cancel, stream, false, metrics, p.newTurnConductor(metrics), repair); err != nil {
				t.Fatal(err)
			}
			_, _, routed := repair.routedKind()
			if routed != tc.routed {
				t.Fatalf("routed = %t, want %t", routed, tc.routed)
			}
			select {
			case e := <-shown:
				if routed {
					t.Fatalf("image shown before the reply routed to a worker: %+v", e)
				}
			default:
				if tc.early {
					t.Fatal("unrouted image reply was not shown early")
				}
			}
		})
	}
}
