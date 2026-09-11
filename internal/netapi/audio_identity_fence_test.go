package netapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func testAudioEnvelope() events.ResponseEnvelope {
	return events.ResponseEnvelope{
		SessionID:         "session-a",
		ConversationEpoch: "epoch-1",
		ResponseID:        "response-unchanged-persona",
		PersonaID:         "santa",
		DisplayName:       "Santa",
		PromptHash:        "prompt-hash",
		BindingHash:       "binding-hash",
		SelectionRevision: 9,
		VoiceProvider:     "kokoro",
		Voice:             "santa-voice",
	}
}

func TestAudioJSONCarriesFullResponseIdentityAndGeneration(t *testing.T) {
	identity := testAudioEnvelope()
	msg, err := marshalAudioChunk(7, 24000, []byte{1, 2}, audioWireIdentity{generation: 12, envelope: identity})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(msg, &got); err != nil {
		t.Fatal(err)
	}
	if got["response_id"] != identity.ResponseID || got["generation"] != float64(12) {
		t.Fatalf("wire identity = %v", got)
	}
	var envelope events.ResponseEnvelope
	encoded, _ := json.Marshal(got["response_envelope"])
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope != identity {
		t.Fatalf("response envelope = %+v, want %+v", envelope, identity)
	}
	data, _ := base64.StdEncoding.DecodeString(got["data"].(string))
	if string(data) != string([]byte{1, 2}) {
		t.Fatalf("pcm = %v", data)
	}
}

func TestOVA2CarriesFullStringResponseIDAndGeneration(t *testing.T) {
	responseID := "response-id-without-a-hash-or-truncation"
	raw := marshalBinaryAudioChunkV2(8, 16000, 21, responseID, []byte{3, 4, 5})
	seg, rate, generation, gotID, pcm, ok := ParseBinaryAudioChunkV2(raw)
	if !ok || seg != 8 || rate != 16000 || generation != 21 || gotID != responseID || string(pcm) != string([]byte{3, 4, 5}) {
		t.Fatalf("OVA2 parse = seg=%d rate=%d generation=%d id=%q pcm=%v ok=%v", seg, rate, generation, gotID, pcm, ok)
	}
	if _, _, _, ok := ParseBinaryAudioChunk(raw); ok {
		t.Fatal("OVA2 was accepted as OVA1")
	}
}

func TestAudioOutputNegotiatesOVA2PerClientAndKeepsOVA1(t *testing.T) {
	h := newHub()
	legacy, v2 := newTestConn(4, 4), newTestConn(4, 4)
	if !h.add(legacy) || !h.add(v2) {
		t.Fatal("add connections")
	}
	h.setConnAudio(legacy, true, true)
	h.setConnAudioProtocol(v2, true, true, "ova2")
	identity := testAudioEnvelope()
	jsonMsg, _ := marshalAudioChunk(1, 24000, []byte{7, 8}, audioWireIdentity{generation: 4, envelope: identity})
	ova1 := marshalBinaryAudioChunk(1, 24000, []byte{7, 8})
	ova2 := marshalBinaryAudioChunkV2(1, 24000, 4, identity.ResponseID, []byte{7, 8})
	if got := h.broadcastPCMAt(jsonMsg, ova1, ova2, h.audioGenerationSnapshot()); got != 2 {
		t.Fatalf("accepted clients = %d, want 2", got)
	}
	if got := <-legacy.audio; string(got[:4]) != "OVA1" {
		t.Fatalf("legacy frame magic = %q", got[:4])
	}
	if got := <-v2.audio; string(got[:4]) != "OVA2" {
		t.Fatalf("OVA2 frame magic = %q", got[:4])
	}
}

func TestHubAudioGenerationRejectsDelayedOldChunkAndEnd(t *testing.T) {
	h := newHub()
	c := newTestConn(8, 8)
	if !h.add(c) {
		t.Fatal("add connection")
	}
	h.setConnAudio(c, true, false)
	oldGeneration := h.audioGenerationSnapshot()
	h.resetAudio()
	select {
	case <-c.audio:
	default:
		t.Fatal("reset marker missing")
	}
	if got := h.broadcastPCMAt([]byte("old-json"), []byte("old-ova1"), []byte("old-ova2"), oldGeneration); got != 0 {
		t.Fatalf("old chunk accepted after reset: %d", got)
	}
	if got := h.broadcastAudio([]byte("old-end"), oldGeneration); got != 0 {
		t.Fatalf("old end accepted after reset: %d", got)
	}
	h.resumeAudio()
	newGeneration := h.audioGenerationSnapshot()
	if newGeneration <= oldGeneration {
		t.Fatalf("generation did not advance across reset/resume: old=%d new=%d", oldGeneration, newGeneration)
	}
	if got := h.broadcastPCMAt([]byte("new-json"), []byte("new-ova1"), []byte("new-ova2"), newGeneration); got != 1 {
		t.Fatalf("new chunk accepted=%d, want 1", got)
	}
	if got := <-c.audio; string(got) != "new-json" {
		t.Fatalf("new chunk = %q", got)
	}
}

func TestAudioOptInDuringResetInheritsBlockedBoundary(t *testing.T) {
	h := newHub()
	c := newTestConn(4, 4)
	if !h.add(c) {
		t.Fatal("add connection")
	}
	h.resetAudio()
	h.setConnAudio(c, true, false)
	if got := h.broadcastAudio([]byte("blocked"), h.audioGenerationSnapshot()); got != 0 {
		t.Fatalf("opt-in during reset accepted audio=%d", got)
	}
	generation := h.resumeAudio()
	if got := h.broadcastAudio([]byte("resumed"), generation); got != 1 {
		t.Fatalf("resumed opt-in accepted audio=%d", got)
	}
}

func TestAttributedBareAckDoesNotReopenBlockedAudioBoundary(t *testing.T) {
	h := newHub()
	bus := events.NewBus()
	detach := h.attachBus(bus)
	defer detach()
	c := newTestConn(8, 8)
	if !h.add(c) {
		t.Fatal("add connection")
	}
	h.setConnAudio(c, true, false)
	h.resetAudio()
	select {
	case <-c.audio:
	default:
		t.Fatal("reset marker missing")
	}
	old := h.audioGenerationSnapshot()
	identity := testAudioEnvelope()
	bus.Emit(events.SpeechSegmentReady{Envelope: &identity, Text: "Santa here."})
	if got := h.audioGenerationSnapshot(); got != old {
		t.Fatalf("bare ack changed generation: got=%d want=%d", got, old)
	}
	if got := h.broadcastAudio([]byte("bare-ack-audio"), old); got != 0 {
		t.Fatalf("bare ack audio accepted=%d while blocked", got)
	}
	newGeneration := h.resumeAudio()
	if newGeneration <= old {
		t.Fatalf("dispatcher resume did not advance generation: old=%d new=%d", old, newGeneration)
	}
	if got := h.broadcastAudio([]byte("resumed-audio"), newGeneration); got != 1 {
		t.Fatalf("resumed audio accepted=%d, want 1", got)
	}
}

type audioGenerationRunner struct {
	ctx chan context.Context
}

func (r *audioGenerationRunner) RunTurnTextMode(ctx context.Context, _ string) error {
	r.ctx <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func TestDispatcherCarriesResumedAudioGeneration(t *testing.T) {
	h := newHub()
	runner := &audioGenerationRunner{ctx: make(chan context.Context, 1)}
	d := NewDispatcher(runner, events.NewBus(), nil, nil)
	d.setAudioLifecycle(h.resumeAudio, h.resetAudio)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(runCtx)
	if err := d.SubmitText("generation"); err != nil {
		t.Fatal(err)
	}
	var turnCtx context.Context
	select {
	case turnCtx = <-runner.ctx:
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	if got, want := audioGenerationFromContext(turnCtx), h.audioGenerationSnapshot(); got != want {
		t.Fatalf("turn generation = %d, hub generation = %d", got, want)
	}
	d.Interrupt()
}

func TestFanoutUsesAdmittedGenerationAcrossInterruptedTurn(t *testing.T) {
	h := newHub()
	bus := events.NewBus()
	detach := h.attachBus(bus)
	defer detach()
	jsonConn, binaryConn := newTestConn(16, 16), newTestConn(16, 16)
	if !h.add(jsonConn) || !h.add(binaryConn) {
		t.Fatal("add connections")
	}
	h.setConnAudio(jsonConn, true, false)
	h.setConnAudioProtocol(binaryConn, true, true, "ova2")

	runner := &audioGenerationRunner{ctx: make(chan context.Context, 1)}
	d := NewDispatcher(runner, bus, nil, nil)
	d.setAudioLifecycle(h.resumeAudio, h.resetAudio)
	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		d.Run(runCtx)
		close(runDone)
	}()
	defer func() {
		cancel()
		<-runDone
	}()

	oldEnvelope := testAudioEnvelope()
	if err := d.SubmitText("old"); err != nil {
		t.Fatal(err)
	}
	oldTurnCtx := <-runner.ctx
	oldAudioCtx := events.WithResponse(context.WithoutCancel(oldTurnCtx), oldEnvelope)
	oldGeneration := audioGenerationFromContext(oldTurnCtx)
	if oldGeneration == 0 {
		t.Fatal("old turn did not receive an audio generation")
	}

	d.Interrupt()
	for _, c := range []*streamConn{jsonConn, binaryConn} {
		select {
		case got := <-c.audio:
			if string(got) != string(audioResetEnvelope) {
				t.Fatalf("reset marker = %q", got)
			}
		default:
			t.Fatal("audio reset marker missing")
		}
	}
	resetGeneration := h.audioGenerationSnapshot()
	if resetGeneration <= oldGeneration {
		t.Fatalf("reset generation = %d, old = %d", resetGeneration, oldGeneration)
	}

	// These are late events from the canceled response. They must not become
	// the authority that reopens the reset boundary before the next admission.
	bus.Emit(events.ThinkingStarted{Envelope: &oldEnvelope})
	bus.Emit(events.SpeechSegmentReady{Envelope: &oldEnvelope, Text: "late old"})
	if got := h.audioGenerationSnapshot(); got != resetGeneration {
		t.Fatalf("late old event changed generation: got=%d want=%d", got, resetGeneration)
	}
	if !h.audioBlocked {
		t.Fatal("late old event reopened the audio boundary")
	}

	if err := d.SubmitText("new"); err != nil {
		t.Fatal(err)
	}
	newTurnCtx := <-runner.ctx
	newGeneration := audioGenerationFromContext(newTurnCtx)
	if newGeneration <= resetGeneration {
		t.Fatalf("new generation = %d, reset = %d", newGeneration, resetGeneration)
	}

	// The provider ignores cancellation, but the immutable admission token is
	// preserved. The old producer must not inherit the next generation.
	oldStream := bufferedPCM(t, oldAudioCtx, audio.SampleRate, []float32{0.8, -0.8})
	fanout := NewAudioFanout(nil)
	fanout.AttachHub(h)
	oldPlayback, err := fanout.PlayStream(oldAudioCtx, oldStream)
	if err != nil {
		t.Fatal(err)
	}
	<-oldPlayback.Done()
	for _, c := range []*streamConn{jsonConn, binaryConn} {
		select {
		case got := <-c.audio:
			t.Fatalf("late old audio crossed boundary: %q", got)
		default:
		}
	}

	newEnvelope := oldEnvelope
	newEnvelope.ResponseID = "response-new"
	newAudioCtx := events.WithResponse(newTurnCtx, newEnvelope)
	newStream := bufferedPCM(t, newAudioCtx, audio.SampleRate, []float32{0.2, -0.2})
	newPlayback, err := fanout.PlayStream(newAudioCtx, newStream)
	if err != nil {
		t.Fatal(err)
	}
	<-newPlayback.Done()

	var jsonChunk map[string]any
	select {
	case raw := <-jsonConn.audio:
		if err := json.Unmarshal(raw, &jsonChunk); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("new JSON audio was not delivered")
	}
	if jsonChunk["type"] != "audio_chunk" || jsonChunk["response_id"] != newEnvelope.ResponseID || jsonChunk["generation"] != float64(newGeneration) {
		t.Fatalf("new JSON chunk = %v", jsonChunk)
	}
	select {
	case raw := <-binaryConn.audio:
		_, rate, generation, responseID, pcm, ok := ParseBinaryAudioChunkV2(raw)
		if !ok || rate != audio.SampleRate || generation != newGeneration || responseID != newEnvelope.ResponseID || len(pcm) != 4 {
			t.Fatalf("new OVA2 chunk ok=%v rate=%d generation=%d responseID=%q pcm=%d", ok, rate, generation, responseID, len(pcm))
		}
	case <-time.After(time.Second):
		t.Fatal("new OVA2 audio was not delivered")
	}
	d.Interrupt()
}

func TestFanoutCapturesResponseBeforePCMReady(t *testing.T) {
	h := newHub()
	c := newTestConn(8, 8)
	if !h.add(c) {
		t.Fatal("add connection")
	}
	h.setConnAudio(c, true, false)
	fanout := NewAudioFanout(nil)
	fanout.AttachHub(h)
	ctx := events.WithResponse(context.Background(), testAudioEnvelope())
	stream := bufferedPCM(t, ctx, audio.SampleRate, []float32{0.25, -0.25})
	playback, err := fanout.PlayStream(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	<-playback.Done()
	var got map[string]any
	for {
		msg := <-c.audio
		if json.Unmarshal(msg, &got) == nil && got["type"] == "audio_chunk" {
			break
		}
	}
	if got["response_id"] != testAudioEnvelope().ResponseID || got["generation"] == nil {
		t.Fatalf("chunk identity = %v", got)
	}
}
