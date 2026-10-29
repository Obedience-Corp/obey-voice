package main

import (
	"context"
	"errors"

	engine "github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd"
)

type voicePreview struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (h *host) stopPreview() {
	if h.preview != nil {
		h.preview.cancel()
	}
}

// The protocol loop stays responsive while assets, synthesis and audio run.
// Replacements drain their predecessor before opening another provider/player.
func (h *host) startPreview(ctx context.Context, r request, o *output) {
	previous := h.preview
	h.stopPreview()
	ctx, cancel := context.WithCancel(ctx)
	p := &voicePreview{cancel: cancel, done: make(chan struct{})}
	h.preview = p
	run := h.previewVoice
	if run == nil {
		run = engine.PreviewDesktopVoice
	}
	go func() {
		defer close(p.done)
		defer cancel()
		if previous != nil {
			<-previous.done
		}
		err := ctx.Err()
		if err == nil {
			err = run(ctx, r.VoiceSelection)
		}
		message := "Preview finished"
		failed := false
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			message = "Preview stopped"
		} else if err != nil {
			message, failed = err.Error(), true
		}
		o.event("persona_preview_done", map[string]any{"id": r.ID, "message": message, "error": failed})
	}()
}
