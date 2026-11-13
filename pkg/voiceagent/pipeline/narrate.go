package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/render"
	"github.com/Obedience-Corp/obey-voice/internal/render/extractors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

const (
	defaultNarrateAck = "I'll read that."
	storyWriterPrompt = "Write a long original story as plain text for reading aloud. " +
		"Use several chapters with clear section breaks. No markdown and no meta commentary. " +
		"Return only the story text."
)

var narrateURLPattern = regexp.MustCompile(`https?://[^\s]+`)

// narrateUnit is one section/chapter of a live narration plan.
type narrateUnit struct {
	spans []render.RenderSpan
}

// Narrator runs live narration mode: render spans through the existing TTS player.
type Narrator struct {
	agency   *Agency
	pipeline *Pipeline
	cfg      *config.Config

	// planSource resolves a URL or file into narration units. Tests override this.
	planSource func(ctx context.Context, source string) ([]narrateUnit, error)

	mu sync.Mutex

	active      bool
	paused      bool
	resumeCh    chan struct{}
	cancel      context.CancelFunc
	units       []narrateUnit
	unitIdx     int
	spanIdx     int
	storyJobID  string
	pendingPath string
}

// NewNarrator constructs the live narration coordinator.
func NewNarrator(a *Agency) *Narrator {
	if a == nil {
		return nil
	}
	return &Narrator{
		agency:   a,
		pipeline: a.pipeline,
		cfg:      a.cfg,
	}
}

// Wire registers narration mode guards on the pipeline.
func (n *Narrator) Wire() {
	if n == nil || n.pipeline == nil {
		return
	}
	n.pipeline.NarrationActive = n.IsActive
}

// IsActive reports whether narration mode is engaged.
func (n *Narrator) IsActive() bool {
	if n == nil {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.active
}

// HandleRoute executes narrate control routes. Agency.HandleRoute owns the
// OnRoute notification; nothing here fires it.
func (n *Narrator) HandleRoute(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	if n == nil {
		return
	}
	switch d.Kind {
	case RouteNarrate:
		n.handleStart(ctx, turn, metrics, d)
	case RouteNarratePause:
		n.handlePause()
	case RouteNarrateResume:
		n.handleResume()
	case RouteNarrateSkip:
		n.handleSkip()
	case RouteNarrateStop:
		n.handleStop()
	}
}

// handleJobDone returns true when the job was a spawn-then-narrate story job.
func (n *Narrator) handleJobDone(id string, state job.State, result string, err error) bool {
	n.mu.Lock()
	storyID := n.storyJobID
	n.mu.Unlock()
	if storyID == "" || id != storyID {
		return false
	}

	n.mu.Lock()
	n.storyJobID = ""
	n.mu.Unlock()

	if state != job.StateDone {
		msg := "That did not work."
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			msg = err.Error()
		}
		n.pipeline.emit(events.Error{Stage: "narrate", Message: msg})
		return true
	}

	path := strings.TrimSpace(result)
	if path == "" {
		n.pipeline.emit(events.Error{Stage: "narrate", Message: "Story job completed without a narration source"})
		return true
	}
	n.tryStartNarration(context.Background(), path)
	n.tryFlushPending()
	go n.retryFlushPending()
	return true
}

func (n *Narrator) retryFlushPending() {
	for i := 0; i < 250; i++ {
		n.tryFlushPending()
		n.mu.Lock()
		pending := n.pendingPath
		active := n.active
		n.mu.Unlock()
		if active || pending == "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (n *Narrator) handleStart(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	if wantsWrittenStory(d.Transcript, n.workDir()) {
		n.mu.Lock()
		writing := n.storyJobID != ""
		n.mu.Unlock()
		if writing {
			// A second job would replace the first one's id and its finished
			// story would then be dropped unread.
			if err := n.pipeline.speakStandalone(ctx, turn, metrics, storyInProgressLine); err != nil {
				n.pipeline.emit(events.Info{Message: fmt.Sprintf("story notice failed: %v", err)})
			}
			return
		}
		n.startStoryJob(ctx, metrics, d.Tier)
		return
	}

	source := resolveNarrateSource(d.Transcript, n.workDir())
	if source == "" {
		// The model chose reading aloud for a request that names nothing to
		// read. Ask, as a person would, instead of failing the turn.
		n.pipeline.emit(events.Info{Message: "narrate: the request names no file or link to read"})
		if err := n.pipeline.speakStandalone(ctx, turn, metrics, narrateSourceQuestion); err != nil {
			n.pipeline.emit(events.Info{Message: fmt.Sprintf("narrate question failed: %v", err)})
		}
		return
	}

	if err := n.pipeline.speakStandalone(ctx, turn, metrics, n.narrateAck()); err != nil {
		n.pipeline.emit(events.Info{Message: fmt.Sprintf("narrate ack failed: %v", err)})
	}
	n.tryStartNarration(ctx, source)
}

// startStoryJob writes the story on the persona's own backend when it does
// its own work, under the same gates as its other own jobs: the policy and the
// routed tier, the hosted notice first, the own-job time cap and the admission
// record. Otherwise the configured worker writes it. Story jobs never start
// under the experimental inbox (HandleRoute refuses them), so none is
// registered with it.
func (n *Narrator) startStoryJob(ctx context.Context, metrics *turnMetrics, tier config.WorkerTierName) {
	work := n.agency.turnWork(ctx)
	spec := job.Spec{Task: "write a long story", Run: n.storyWriterRun(nil, nil)}
	if work.own != nil {
		ownTier, err := work.admit(tier)
		if err != nil {
			n.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
			return
		}
		if brain.IsHosted(providerName(work.own)) {
			if err := n.pipeline.announceHosted(ctx, metrics); err != nil {
				return
			}
		}
		admission := frozenWorkerConfig(work.own, nil)
		admission["work"] = config.WorkOwn
		if ownTier != nil {
			admission["worker_tier"] = string(ownTier.Tier)
		}
		origin := workerOrigin{admission: admission}
		if metrics != nil {
			origin.Envelope = metrics.response
		}
		spec.Provider, spec.Model = providerName(work.own), modelName(work.own)
		spec.RuntimeBudget = n.agency.ownRuntimeBudget(work, ownTier)
		spec.Run = n.storyWriterRun(work.own, ownTier)
		spec.OnAccepted = func(id string) {
			n.agency.mu.Lock()
			n.agency.workerOrigins[id] = origin
			n.agency.mu.Unlock()
		}
	}
	id, err := n.agency.jobs.Start(ctx, spec)
	if err != nil {
		n.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
		return
	}
	n.mu.Lock()
	n.storyJobID = id
	n.mu.Unlock()
	n.agency.noteOwnWorkDefault(work)
}

func (n *Narrator) storyWriterRun(own *config.Config, tier *config.OwnTier) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		opts := brain.WorkerStreamOptions(n.cfg == nil || n.cfg.VoiceToolsEnabled)
		var worker brain.Provider
		if own != nil {
			if end, ok := job.BudgetEnd(ctx); ok {
				ctx = brain.WithWorkerDeadline(ctx, end)
			}
			opts.ToolsEnabled = own.VoiceToolsEnabled
			if opts.WorkerBudget = ownTierBudget(own, tier); opts.WorkerBudget != nil {
				ctx = opts.WorkerBudget.Context(ctx)
			}
			built, err := n.agency.buildOwnWorker(ctx, own)
			if err != nil {
				return "", err
			}
			defer func() { _ = brain.CloseProvider(built) }()
			worker = built
		} else {
			configured, err := n.agency.workerProvider()
			if err != nil {
				return "", err
			}
			worker = configured
		}
		raw, err := worker.ThinkFull(ctx, storyWriterPrompt, opts)
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(n.agency.cleanSummary(raw))
		if text == "" {
			return "", fmt.Errorf("story writer returned nothing")
		}
		dir := n.workDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create narration dir: %w", err)
		}
		path := filepath.Join(dir, fmt.Sprintf("narration-story-%d.txt", time.Now().UnixNano()))
		if err := os.WriteFile(path, []byte(text+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("write story: %w", err)
		}
		return path, nil
	}
}

func (n *Narrator) tryStartNarration(ctx context.Context, source string) {
	if n.canStartPlayback() && !n.IsActive() {
		n.startPlayback(ctx, source)
		return
	}
	n.mu.Lock()
	n.pendingPath = source
	n.mu.Unlock()
}

func (n *Narrator) tryFlushPending() {
	n.mu.Lock()
	path := n.pendingPath
	active := n.active
	n.mu.Unlock()
	if path == "" || active {
		return
	}
	if !n.canStartPlayback() {
		return
	}
	n.mu.Lock()
	if n.pendingPath != path || n.active {
		n.mu.Unlock()
		return
	}
	n.pendingPath = ""
	n.mu.Unlock()
	n.startPlayback(context.Background(), path)
}

func (n *Narrator) canStartPlayback() bool {
	if n.pipeline.activeTurns.Load() > 0 {
		return false
	}
	if n.pipeline.Player != nil && n.pipeline.Player.IsPlaying() {
		return false
	}
	return true
}

func (n *Narrator) startPlayback(ctx context.Context, source string) {
	units, err := n.loadPlan(ctx, source)
	if err != nil || len(units) == 0 {
		message := "Narration source contains no readable content"
		if err != nil {
			message = err.Error()
		}
		n.pipeline.emit(events.Error{Stage: "narrate", Message: message})
		return
	}

	n.mu.Lock()
	if n.cancel != nil {
		n.cancel()
	}
	playCtx, cancel := context.WithCancel(context.Background())
	n.active = true
	n.paused = false
	n.resumeCh = make(chan struct{}, 1)
	n.units = units
	n.unitIdx = 0
	n.spanIdx = 0
	n.pendingPath = ""
	n.cancel = cancel
	n.mu.Unlock()

	go n.playLoop(playCtx, cancel)
}

func (n *Narrator) loadPlan(ctx context.Context, source string) ([]narrateUnit, error) {
	if n.planSource != nil {
		return n.planSource(ctx, source)
	}
	return planLiveNarration(ctx, source)
}

func (n *Narrator) playLoop(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	defer func() {
		n.mu.Lock()
		n.active = false
		n.paused = false
		n.units = nil
		n.unitIdx = 0
		n.spanIdx = 0
		n.cancel = nil
		path := n.pendingPath
		n.pendingPath = ""
		n.mu.Unlock()
		if path != "" && n.canStartPlayback() {
			n.startPlayback(context.Background(), path)
		}
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		n.waitForTurnGap(ctx)

		if n.waitIfPaused(ctx) {
			return
		}

		unit, span, unitIdx, spanIdx, ok := n.currentSpan()
		if !ok {
			return
		}

		switch span.Kind {
		case render.SpanSilence:
			if !n.waitSilence(ctx, span.Duration) {
				return
			}
			n.advanceSpan(unitIdx, spanIdx, len(unit.spans))
		case render.SpanSpeech:
			text := strings.TrimSpace(span.Text)
			if text == "" {
				n.advanceSpan(unitIdx, spanIdx, len(unit.spans))
				continue
			}
			cleaned := brain.CleanForVoice(text)
			if !speakableSegment(cleaned) {
				n.advanceSpan(unitIdx, spanIdx, len(unit.spans))
				continue
			}
			err := n.pipeline.speakStandalone(ctx, nil, nil, cleaned)
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errPlaybackInterrupted) {
				continue
			}
			n.advanceSpan(unitIdx, spanIdx, len(unit.spans))
		default:
			n.advanceSpan(unitIdx, spanIdx, len(unit.spans))
		}
	}
}

func (n *Narrator) waitForTurnGap(ctx context.Context) {
	for n.pipeline.activeTurns.Load() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (n *Narrator) waitIfPaused(ctx context.Context) bool {
	for {
		n.mu.Lock()
		paused := n.paused
		resumeCh := n.resumeCh
		n.mu.Unlock()
		if !paused {
			return false
		}
		select {
		case <-ctx.Done():
			return true
		case <-resumeCh:
		}
	}
}

func (n *Narrator) waitSilence(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (n *Narrator) currentSpan() (narrateUnit, render.RenderSpan, int, int, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for n.unitIdx < len(n.units) {
		unit := n.units[n.unitIdx]
		if n.spanIdx < len(unit.spans) {
			return unit, unit.spans[n.spanIdx], n.unitIdx, n.spanIdx, true
		}
		n.unitIdx++
		n.spanIdx = 0
	}
	return narrateUnit{}, render.RenderSpan{}, 0, 0, false
}

func (n *Narrator) advanceSpan(unitIdx, spanIdx, unitLen int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.unitIdx != unitIdx || n.spanIdx != spanIdx {
		return
	}
	n.spanIdx++
	if n.spanIdx >= unitLen {
		n.unitIdx++
		n.spanIdx = 0
	}
}

func (n *Narrator) handlePause() {
	n.mu.Lock()
	n.paused = true
	n.mu.Unlock()
	n.pipeline.StopPlaybackFrom(StopSource{Name: "narration"})
}

func (n *Narrator) handleResume() {
	n.mu.Lock()
	n.paused = false
	select {
	case n.resumeCh <- struct{}{}:
	default:
	}
	n.mu.Unlock()
}

func (n *Narrator) handleSkip() {
	n.pipeline.StopPlaybackFrom(StopSource{Name: "narration"})
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.units) == 0 {
		return
	}
	if n.unitIdx+1 < len(n.units) {
		n.unitIdx++
		n.spanIdx = 0
		return
	}
	n.skipToNextSpeech()
}

func (n *Narrator) skipToNextSpeech() {
	for n.unitIdx < len(n.units) {
		unit := n.units[n.unitIdx]
		for n.spanIdx < len(unit.spans) {
			if unit.spans[n.spanIdx].Kind == render.SpanSpeech &&
				speakableSegment(brain.CleanForVoice(unit.spans[n.spanIdx].Text)) {
				return
			}
			n.spanIdx++
		}
		n.unitIdx++
		n.spanIdx = 0
	}
}

func (n *Narrator) handleStop() {
	n.mu.Lock()
	cancel := n.cancel
	n.active = false
	n.paused = false
	n.units = nil
	n.unitIdx = 0
	n.spanIdx = 0
	n.pendingPath = ""
	n.storyJobID = ""
	n.mu.Unlock()
	n.pipeline.StopPlaybackFrom(StopSource{Name: "narration"})
	if cancel != nil {
		cancel()
	}
}

func (n *Narrator) narrateAck() string {
	if n.cfg != nil && strings.TrimSpace(n.cfg.ActivePersona) == "jarvis" {
		return "I'll read that, sir."
	}
	return defaultNarrateAck
}

func (n *Narrator) workDir() string {
	if n.cfg != nil {
		if dir := config.WorkDirFrom(n.cfg); dir != "" {
			return dir
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return os.TempDir()
	}
	return wd
}

func isLongStoryPhrase(transcript string) bool {
	return normalizeUtterance(transcript) == "tell me a long story"
}

// narrateSourceQuestion is spoken when a reading request names nothing to read.
const narrateSourceQuestion = "What should I read? Give me a file or a link, or ask me for a long story."

// storyInProgressLine answers a story request made while one is being written.
const storyInProgressLine = "I'm still writing your story. I'll read it as soon as it's ready."

var (
	storyWord      = regexp.MustCompile(`\b(story|stories|tale|tales)\b`)
	shortStoryWord = regexp.MustCompile(`\b(short|quick|brief|little|tiny)\b`)
)

// wantsWrittenStory reports a reading request for a story that does not exist
// yet: it asks for a story and names no file or link. The exact phrase "tell me
// a long story" is one such request; "can you tell me a longer story?" is
// another, and before this it ended in "No narration source found".
func wantsWrittenStory(transcript, workDir string) bool {
	if isLongStoryPhrase(transcript) {
		return true
	}
	said := normalizeUtterance(transcript)
	// The writer produces several chapters, which is not what a short story
	// request asks for.
	if !storyWord.MatchString(said) || shortStoryWord.MatchString(said) {
		return false
	}
	return resolveNarrateSource(transcript, workDir) == ""
}

func resolveNarrateSource(transcript, workDir string) string {
	if url := extractNarrateURL(transcript); url != "" {
		return url
	}
	if path := extractNarratePath(transcript, workDir); path != "" {
		return path
	}
	return ""
}

func extractNarrateURL(s string) string {
	match := narrateURLPattern.FindString(s)
	if match == "" {
		return ""
	}
	return strings.TrimRight(match, ".,;:!?)\"']")
}

func extractNarratePath(transcript, workDir string) string {
	trimmed := strings.TrimSpace(transcript)
	if trimmed == "" {
		return ""
	}
	candidates := []string{trimmed}
	candidates = append(candidates, strings.Fields(trimmed)...)
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		c = strings.Trim(c, "\"'")
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		path := c
		if !filepath.IsAbs(path) && workDir != "" {
			path = filepath.Join(workDir, c)
		}
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	return ""
}

func planLiveNarration(ctx context.Context, source string) ([]narrateUnit, error) {
	opts := render.Options{Input: source, Format: render.FormatAuto}
	format := opts.ResolveFormat()
	switch format {
	case render.FormatText:
		data, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			return nil, fmt.Errorf("empty file")
		}
		spans := render.PlanTextSpans(opts, text)
		if len(spans) == 0 {
			return nil, fmt.Errorf("no readable text")
		}
		return []narrateUnit{{spans: spans}}, nil
	case render.FormatMarkdown, render.FormatHTML, render.FormatURL:
		doc, err := extractLiveDocument(ctx, source, format)
		if err != nil {
			return nil, err
		}
		rawUnits := doc.Units()
		if len(rawUnits) == 0 {
			return nil, fmt.Errorf("no readable text")
		}
		units := make([]narrateUnit, 0, len(rawUnits))
		for _, u := range rawUnits {
			text := strings.TrimSpace(u.Text)
			if text == "" {
				continue
			}
			spans := render.PlanUnitSpans(opts, text)
			if len(spans) == 0 {
				continue
			}
			units = append(units, narrateUnit{spans: spans})
		}
		if len(units) == 0 {
			return nil, fmt.Errorf("no readable text")
		}
		return units, nil
	default:
		return nil, fmt.Errorf("unsupported source format %q", format)
	}
}

func extractLiveDocument(ctx context.Context, source string, format render.Format) (render.Document, error) {
	switch format {
	case render.FormatMarkdown:
		data, err := os.ReadFile(source)
		if err != nil {
			return render.Document{}, fmt.Errorf("read markdown: %w", err)
		}
		return extractors.ExtractMarkdownPolicy(source, data, "skip")
	case render.FormatHTML:
		data, err := os.ReadFile(source)
		if err != nil {
			return render.Document{}, fmt.Errorf("read html: %w", err)
		}
		return extractors.ExtractHTML(source, data)
	case render.FormatURL:
		data, err := extractors.FetchArticle(ctx, nil, source, extractors.FetchOptions{})
		if err != nil {
			return render.Document{}, fmt.Errorf("fetch url: %w", err)
		}
		return extractors.ExtractHTML(source, data)
	default:
		return render.Document{}, fmt.Errorf("unsupported document format %q", format)
	}
}
