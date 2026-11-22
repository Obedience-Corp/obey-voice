package brain

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// tightBrain is a conversation whose context holds only a few hundred tokens.
func tightBrain(t *testing.T, fake *visionFake, numCtx int) *OllamaBrain {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := budgetConfig()
	cfg.OllamaNumCtx, cfg.ContinuityNumCtxCeil = numCtx, numCtx
	return fake.brain(t, cfg, "You are Obey.")
}

// longRequest asks for a chart in about as many bytes as fill the context
// of numCtx tokens a little short of its limit.
func longRequest(numCtx int) string {
	request := "Draw me a chart of the year."
	for len(request) < (numCtx*9/10-85)*4 {
		request += " The numbers go up in spring and fall away by autumn."
	}
	return request
}

// A picture posted after a long request stays with the next user message when
// the context trims the request and the reply away.
func TestPostedPictureSurvivesATrim(t *testing.T) {
	for _, numCtx := range []int{300, 325, 350} {
		t.Run(strconv.Itoa(numCtx), func(t *testing.T) {
			fake := &visionFake{}
			o := tightBrain(t, fake, numCtx)
			ref := photos(t, 1, 32, 32)[0]
			ref.Name = "chart.png"
			if _, err := o.ThinkFull(t.Context(), longRequest(numCtx), StreamOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := o.SetReplyMedia("Here is the chart.\nchart.png", []chatmedia.Ref{ref}); err != nil {
				t.Fatal(err)
			}
			if _, err := o.ThinkFull(t.Context(), "What colour is it?", StreamOptions{}); err != nil {
				t.Fatal(err)
			}
			req := fake.sent()[len(fake.sent())-1]
			if strings.Contains(jsonOf(t, req.Messages), "Draw me a chart") {
				t.Fatal("the request was not trimmed")
			}
			if m := messageWith(t, req, "What colour is it?"); len(m.Images) != 1 || strings.Count(m.Content, pictureLine(ref, attached, postedByYou)) != 1 {
				t.Fatalf("after the trim the question carries %d images: %q", len(m.Images), m.Content)
			}
		})
	}
}

// A picture posted before the message being answered stays with it when the
// conversation is compacted for a continuation, and after a persona switch.
func TestPostedPictureSurvivesCompaction(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	if _, err := o.ThinkFull(t.Context(), "What colour is it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := o.compactForContinuation(t.Context()); err != nil {
		t.Fatal(err)
	}
	req := built(o)
	if strings.Contains(jsonOf(t, req.Messages), "Draw me a chart") {
		t.Fatal("the conversation was not compacted")
	}
	if m := messageWith(t, req, "What colour is it?"); len(m.Images) != 1 || strings.Count(m.Content, pictureLine(ref, attached, postedByYou)) != 1 {
		t.Fatalf("after compaction the question carries %d images: %q", len(m.Images), m.Content)
	}
	// A persona switch takes the compacted view with what its messages carry.
	r := (&visionFake{}).brain(t, budgetConfig(), "You are Obey.")
	if err := r.ImportContinuity(o.ContinuitySnapshot()); err != nil {
		t.Fatal(err)
	}
	if m := messageWith(t, built(r), "What colour is it?"); strings.Count(m.Content, pictureNamed(ref)) != 1 {
		t.Fatalf("after a switch the question reads %q", m.Content)
	}
}

// budgetFailures counts, over a range of context sizes, the turns refused for
// the prompt budget after six turns and a reply that posted four pictures, or
// none when pictures is false.
func budgetFailures(t *testing.T, pictures bool) (failures, tried int) {
	t.Helper()
	for numCtx := 250; numCtx < 700; numCtx += 3 {
		fake := &visionFake{}
		o := tightBrain(t, fake, numCtx)
		refs := photos(t, 4, 32, 32)
		ok := true
		for i := range 6 {
			if _, err := o.ThinkFull(t.Context(), "filler turn "+strconv.Itoa(i)+" about the garden and the weather this week", StreamOptions{}); err != nil {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if pictures {
			if err := o.SetReplyMedia("Here they are.\npictures", refs); err != nil {
				t.Fatal(err)
			}
		}
		tried++
		_, err := o.ThinkFull(t.Context(), "Which of them do you like best?", StreamOptions{})
		if errors.Is(err, ErrPromptBudget) {
			failures++
		} else if err != nil {
			t.Fatalf("num_ctx %d: %v", numCtx, err)
		}
	}
	return failures, tried
}

// The lines naming carried pictures count toward the context estimate, so a
// conversation near its limit trims instead of being refused.
func TestCarriedPictureLinesCountTowardTheContextEstimate(t *testing.T) {
	plain, plainTried := budgetFailures(t, false)
	withPictures, tried := budgetFailures(t, true)
	if tried != plainTried || tried == 0 {
		t.Fatalf("tried %d sizes with pictures and %d without", tried, plainTried)
	}
	t.Logf("%d of %d sizes refused the turn with pictures, %d without", withPictures, tried, plain)
	if withPictures > plain {
		t.Fatalf("%d of %d sizes refused the turn with pictures, %d without", withPictures, tried, plain)
	}
}

// roundTrip is v after saving it as JSON and reading it back.
func roundTrip[T any](t *testing.T, v T) T {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// trimmedAfterAPostedPicture is a conversation whose working view has lost
// the reply that posted a picture the question after it still carries.
func trimmedAfterAPostedPicture(t *testing.T) (*OllamaBrain, chatmedia.Ref) {
	t.Helper()
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	if _, err := o.ThinkFull(t.Context(), "What colour is it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	carrier := lastUserIndex(o.history)
	o.history, o.working = o.history[carrier:], o.working[carrier:]
	if m := messageWith(t, built(o), "What colour is it?"); strings.Count(m.Content, pictureNamed(ref)) != 1 {
		t.Fatalf("before saving the question reads %q", m.Content)
	}
	return o, ref
}

// A saved persona context keeps what the question carries though the reply
// that posted it had left its working view.
func TestSavedPersonaContextKeepsAPostedPicture(t *testing.T) {
	o, ref := trimmedAfterAPostedPicture(t)
	state := roundTrip(t, CapturePersonaContext(o.ContinuitySnapshot()))
	canonical := roundTrip(t, o.History())
	if !state.Matches(canonical) {
		t.Fatal("the saved context no longer matches the saved conversation")
	}
	r := (&visionFake{}).brain(t, budgetConfig(), "You are Obey.")
	if err := r.ImportContinuity(state.Reconcile(canonical)); err != nil {
		t.Fatal(err)
	}
	if m := messageWith(t, built(r), "What colour is it?"); strings.Count(m.Content, pictureNamed(ref)) != 1 {
		t.Fatalf("after a restart the question reads %q", m.Content)
	}
}

// A saved conversation keeps the picture on the message that carries it, even
// when the turns loaded no longer hold the reply that posted it.
func TestSavedConversationKeepsAPostedPicture(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	if _, err := o.ThinkFull(t.Context(), "What colour is it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	saved := roundTrip(t, o.History())
	if saved[2].Content != "What colour is it?" || len(saved[2].Attachments) != 0 {
		t.Fatalf("the saved question reads %+v", saved[2])
	}
	r := (&visionFake{}).brain(t, budgetConfig(), "You are Obey.")
	r.LoadHistory(saved[2:])
	if m := messageWith(t, built(r), "What colour is it?"); strings.Count(m.Content, pictureNamed(ref)) != 1 {
		t.Fatalf("after loading the question reads %q", m.Content)
	}
}
