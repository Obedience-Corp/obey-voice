package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

type imageRepairWorker struct {
	fakeBrain
	answers   []string
	inputs    []string
	refs      []chatmedia.Ref
	options   []brain.StreamOptions
	cancel    context.CancelFunc
	repairErr error
}

func (*imageRepairWorker) ValidateImages(context.Context, []chatmedia.Ref) error { return nil }

func (w *imageRepairWorker) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	w.inputs = append(w.inputs, input)
	w.options = append(w.options, opts)
	if err := opts.WorkerBudget.AcquireRequest(ctx); err != nil {
		return "", err
	}
	opts.WorkerBudget.RecordUsage(1, 1)
	if w.cancel != nil {
		w.cancel()
	}
	if len(w.inputs) > len(w.answers) {
		return "", errors.New("unexpected worker call")
	}
	if len(w.inputs) == 2 && w.repairErr != nil {
		return "", w.repairErr
	}
	return w.answers[len(w.inputs)-1], nil
}

func (w *imageRepairWorker) SetReplyMedia(_ string, refs []chatmedia.Ref) error {
	w.refs = refs
	return nil
}

func TestWorkerRepairsRejectedImageReferences(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.png"), testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	const bad = "![Example](https://example.invalid/page)"
	const good = "![Example](fixture.png)"
	const offline = "I tried the image search, but it was offline."
	const retryFailed = "The backup source had no matching image."
	const claimed = "I attached the picture."
	for _, tc := range []struct {
		name                      string
		answers                   []string
		wantRefs                  int
		wantErr                   bool
		requests, outputTokens    int
		noTools, cancel, required bool
		repairErr                 error
		wantDetails               []string
		wantText                  string
	}{
		{name: "recovered", answers: []string{bad, good}, wantRefs: 1},
		{name: "required image repaired", answers: []string{offline, good}, required: true, wantRefs: 1},
		{name: "required image has broken link with prose", answers: []string{claimed + "\n" + bad}, required: true, requests: 1, wantErr: true, wantDetails: []string{claimed, "remote image URLs"}},
		{name: "required image repair returns an explanation", answers: []string{offline, retryFailed}, required: true, wantErr: true, wantDetails: []string{offline, retryFailed}},
		{name: "required image repair hits output cap", answers: []string{offline, good}, required: true, outputTokens: 1, wantErr: true, wantDetails: []string{offline, errNoImageDelivered.Error()}},
		{name: "required image repair loses connection", answers: []string{offline, ""}, required: true, repairErr: errors.New("connection closed"), wantErr: true, wantDetails: []string{offline, "connection closed"}},
		{name: "required image repair times out", answers: []string{offline, ""}, required: true, repairErr: &brain.WorkerOutOfTimeError{Partial: "Backup search started."}, wantErr: true, wantDetails: []string{offline, "Backup search started."}},
		{name: "still invalid", answers: []string{bad, bad}, wantErr: true, wantText: "[Image unavailable: Example]"},
		{name: "partial success", answers: []string{bad + "\n" + good}, wantRefs: 1},
		{name: "honest failure", answers: []string{bad, "I couldn't obtain the file."}, wantText: "I couldn't obtain the file."},
		{name: "useful prose", answers: []string{"Here is the full answer.\n" + bad}, wantText: "Here is the full answer.\n[Image unavailable: Example]"},
		{name: "in-sentence reference", answers: []string{"Here it is. " + good}, wantRefs: 1, wantText: "Here it is.\nExample"},
		{name: "in-sentence broken link with useful prose", answers: []string{"Here is the full answer. " + bad}, wantText: "Here is the full answer.\n[Image unavailable: Example]"},
		{name: "required in-sentence broken link with prose", answers: []string{claimed + " " + bad}, required: true, requests: 1, wantErr: true, wantDetails: []string{claimed, "remote image URLs"}},
		{name: "no requests left", answers: []string{bad}, requests: 1, wantErr: true, wantText: "[Image unavailable: Example]"},
		{name: "output cap refuses repair", answers: []string{bad, good}, outputTokens: 1, wantErr: true, wantText: "[Image unavailable: Example]"},
		{name: "tools disabled", answers: []string{bad}, noTools: true, wantErr: true, wantText: "[Image unavailable: Example]"},
		{name: "canceled", answers: []string{bad}, cancel: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{WorkDir: dir, VoiceToolsEnabled: !tc.noTools}
			w := &imageRepairWorker{answers: tc.answers, repairErr: tc.repairErr}
			jobs := job.NewTable(t.Context())
			defer jobs.Close()
			a := NewAgency(&Pipeline{Events: events.NewBus()}, jobs, w, cfg, nil)
			budget := brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: tc.requests, MaxOutputTokens: tc.outputTokens})
			const brief = "Edit this picture"
			refs := []chatmedia.Ref{{ID: "input-image"}}
			ctx, cancel := context.WithCancel(chatmedia.WithInput(t.Context(), refs))
			defer cancel()
			if tc.cancel {
				w.cancel = cancel
			}
			if tc.required {
				ctx = context.WithValue(ctx, imageDeliveryKey{}, true)
			}
			text, err := a.workerRunWithBudget(brief, w, cfg, budget, true)(ctx)
			if tc.wantText != "" && text != tc.wantText {
				t.Fatalf("reply = %q", text)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error = %v", err)
			}
			if tc.wantErr && !tc.cancel && (err == nil || !strings.Contains(err.Error(), "could not attach the worker's images")) {
				t.Fatalf("attachment error = %v", err)
			}
			for _, detail := range tc.wantDetails {
				if err == nil || !strings.Contains(err.Error(), detail) {
					t.Fatalf("job error %v is missing %q", err, detail)
				}
			}
			if tc.repairErr != nil && !errors.Is(err, tc.repairErr) {
				t.Fatalf("lost repair error: %v", err)
			}
			if errors.Is(tc.repairErr, brain.ErrWorkerOutOfTime) {
				outcome := outOfTimeText(0, err)
				for _, detail := range tc.wantDetails {
					if !strings.Contains(outcome, detail) {
						t.Fatalf("timeout outcome %q is missing %q", outcome, detail)
					}
				}
			}
			for _, opts := range w.options {
				if opts.WorkerBudget != budget || !slices.Equal(opts.Attachments, refs) {
					t.Fatal("worker lost budget or attachments")
				}
			}
			if (err != nil) != tc.wantErr || len(w.refs) != tc.wantRefs || len(w.inputs) != len(tc.answers) {
				t.Fatalf("inputs=%v refs=%v err=%v", w.inputs, w.refs, err)
			}
			if len(w.inputs) > 1 {
				reason := errNoImageDelivered.Error()
				if strings.Contains(tc.answers[0], bad) {
					reason = "remote image URLs"
				}
				if !strings.Contains(w.inputs[1], reason) || !strings.Contains(w.inputs[1], brief) {
					t.Fatalf("repair prompt lost its cause or brief: %q", w.inputs[1])
				}
			}
			for _, ref := range w.refs {
				if _, err := chatmedia.DefaultStore().Preview(ref); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPaidWorkerCannotRetryRejectedImage(t *testing.T) {
	cfg := savedPaidConfig(t)
	worker := &imageRepairWorker{answers: []string{"![Example](https://example.invalid/page)"}}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(&Pipeline{Events: events.NewBus()}, jobs, worker, cfg, nil)
	text, err := a.paidWorkerRun(t.Context(), "show image", worker, cfg, spendingPlan(), "req-image")
	if err == nil || !strings.Contains(err.Error(), "remote image URLs") || text != "[Image unavailable: Example]" || len(worker.inputs) != 1 {
		t.Fatalf("calls=%d reply=%q err=%v", len(worker.inputs), text, err)
	}
}

func TestUnbudgetedOllamaWorkerRepairsPromiseBeforePublication(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dog.png"), testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{VoiceToolsEnabled: true, VoiceWorkerProvider: "ollama", WorkDir: dir}
	w := &imageRepairWorker{answers: []string{"I found a dog. Let me add it to the chat.", "![Dog](dog.png)"}}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(&Pipeline{Events: events.NewBus()}, jobs, w, cfg, nil)
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	reply, err := a.workerRun("Get a dog photo and add it to the chat", w, cfg)(ctx)
	if err != nil || len(w.refs) != 1 || len(w.inputs) != 2 || reply != "Dog" {
		t.Fatalf("worker result=%q refs=%v calls=%d error=%v", reply, w.refs, len(w.inputs), err)
	}
}

type cloudAliasWorker struct {
	imageRepairWorker
	checks int
}

func (w *cloudAliasWorker) ValidateLegacyWorker(context.Context) error {
	w.checks++
	return errors.New("ollama model is a remote or cloud alias")
}

func (*cloudAliasWorker) ValidateWorkerTools(context.Context) error { return nil }

// Without the work feature nothing validates the worker at admission, so the
// repair has to establish for itself that a second request stays local.
func TestUnbudgetedCloudAliasWorkerGetsNoSecondRequest(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{VoiceToolsEnabled: true, VoiceWorkerProvider: "ollama", WorkDir: t.TempDir()}
	w := &cloudAliasWorker{imageRepairWorker: imageRepairWorker{answers: []string{"I found a dog. Let me add it to the chat.", "![Dog](dog.png)"}}}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(&Pipeline{Events: events.NewBus()}, jobs, w, cfg, nil)
	if a.workEnabled() {
		t.Fatal("the work feature is on")
	}
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	_, err := a.workerRun("Get a dog photo and add it to the chat", w, cfg)(ctx)
	if !errors.Is(err, errNoImageDelivered) || len(w.inputs) != 1 || w.checks != 1 {
		t.Fatalf("calls=%d locality checks=%d err=%v", len(w.inputs), w.checks, err)
	}
}

type localWorker struct {
	imageRepairWorker
	checks int
}

func (w *localWorker) ValidateLegacyWorker(context.Context) error { w.checks++; return nil }
func (*localWorker) ValidateWorkerTools(context.Context) error    { return nil }

func TestUnbudgetedLocalWorkerRepairsAfterValidating(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dog.png"), testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{VoiceToolsEnabled: true, VoiceWorkerProvider: "ollama", WorkDir: dir}
	w := &localWorker{imageRepairWorker: imageRepairWorker{answers: []string{"I found a dog. Let me add it to the chat.", "![Dog](dog.png)"}}}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(&Pipeline{Events: events.NewBus()}, jobs, w, cfg, nil)
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	reply, err := a.workerRun("Get a dog photo and add it to the chat", w, cfg)(ctx)
	if err != nil || reply != "Dog" || len(w.inputs) != 2 || w.checks != 1 {
		t.Fatalf("reply=%q calls=%d locality checks=%d err=%v", reply, len(w.inputs), w.checks, err)
	}
}
