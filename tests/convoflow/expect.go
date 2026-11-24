package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type turnState struct {
	spoken             string
	response           string
	events             []timedEvent
	route              *pipeline.RouteDecision
	history            []brain.Turn
	liveRouterPolicy   string
	workerRouterPolicy string
	workerToolsEnabled *bool
	liveBrainCalled    bool
	workerCalled       bool
	persona            personaTurnState
	workDir            string
}

func evalExpect(ex Expectation, rec *recorder, st turnState) error {
	switch {
	case ex.Correction != "":
		if !hasCorrection(st.events, rec, ex.Correction) {
			return fmt.Errorf("want a job_correction %q", ex.Correction)
		}
	case ex.WorkFile != "":
		return checkWorkFile(st.workDir, ex.WorkFile, ex.WorkFileContains)
	case ex.CutShort != "":
		if !hasCutShort(st.events, rec, ex.CutShort) {
			return fmt.Errorf("want a job_finished with an answer, cut short: %s", ex.CutShort)
		}
	case ex.JobControl != "":
		return checkJobControl(st.events, rec, ex.JobControl)
	case ex.JobControlAction != "":
		return checkJobControlAction(st.events, rec, ex.JobControlAction)
	case ex.TurnSpokenNotMatches != "":
		re, err := regexp.Compile(ex.TurnSpokenNotMatches)
		if err != nil {
			return err
		}
		src := st.events
		if src == nil && rec != nil {
			src = rec.snapshot()
		}
		if spoken := spokenIn(src); re.MatchString(spoken) {
			return fmt.Errorf("turn spoke %q, matching forbidden %s", clip(spoken, 120), ex.TurnSpokenNotMatches)
		}
	case ex.AbortReason != "":
		if !hasAbortReason(st.events, rec, ex.AbortReason) {
			return fmt.Errorf("want a job_aborted with reason %q", ex.AbortReason)
		}
	case ex.SpokenOnce != "":
		re, err := regexp.Compile(ex.SpokenOnce)
		if err != nil {
			return err
		}
		if n := len(re.FindAllStringIndex(st.spoken, -1)); n != 1 {
			return fmt.Errorf("spoken %q matches %s %d times, want once", clip(st.spoken, 120), ex.SpokenOnce, n)
		}
	case ex.ToolsStopAfterReport != nil:
		late := toolsAfterReport(st.events, rec)
		if late < 0 {
			return fmt.Errorf("want a job_report")
		}
		if (late == 0) != *ex.ToolsStopAfterReport {
			return fmt.Errorf("tool calls started after the report: %d", late)
		}
	case ex.Event != "":
		if !hasNamedEvent(st.events, rec, ex.Event) {
			return fmt.Errorf("want event %q", ex.Event)
		}
	case ex.NoEvent != "":
		if hasNamedEvent(st.events, rec, ex.NoEvent) {
			if ex.NoEvent == "tool_call_started" && onlyRouteToolCalls(st.events, rec) {
				break
			}
			return fmt.Errorf("unexpected event %q", ex.NoEvent)
		}
	case ex.SpokenMatches != "":
		re, err := regexp.Compile(ex.SpokenMatches)
		if err != nil {
			return err
		}
		if !re.MatchString(st.spoken) {
			return fmt.Errorf("spoken %q does not match %s", clip(st.spoken, 120), ex.SpokenMatches)
		}
	case ex.SpokenNotMatches != "":
		re, err := regexp.Compile(ex.SpokenNotMatches)
		if err != nil {
			return err
		}
		if re.MatchString(st.spoken) {
			return fmt.Errorf("spoken matched forbidden %s", ex.SpokenNotMatches)
		}
	case ex.ResponseMatches != "":
		re, err := regexp.Compile(ex.ResponseMatches)
		if err != nil {
			return err
		}
		if !re.MatchString(st.response) {
			return fmt.Errorf("response %q does not match %s", clip(st.response, 120), ex.ResponseMatches)
		}
	case ex.LiveHistoryIncludes != "":
		if !historyContains(st.history, ex.LiveHistoryIncludes) {
			return fmt.Errorf("live history missing %q", ex.LiveHistoryIncludes)
		}
	case ex.LiveHistoryExcludes != "":
		if historyContains(st.history, ex.LiveHistoryExcludes) {
			return fmt.Errorf("live history contains %q", ex.LiveHistoryExcludes)
		}
	case ex.LiveRouterPolicy != "":
		if !st.liveBrainCalled {
			return nil // skip-brain turns do not record live policy
		}
		if st.liveRouterPolicy != ex.LiveRouterPolicy {
			return fmt.Errorf("live_router_policy = %q, want %q", st.liveRouterPolicy, ex.LiveRouterPolicy)
		}
	case ex.WorkerRouterPolicy != "":
		if !st.workerCalled {
			return fmt.Errorf("worker_router_policy: worker was not called")
		}
		if st.workerRouterPolicy != ex.WorkerRouterPolicy {
			return fmt.Errorf("worker_router_policy = %q, want %q", st.workerRouterPolicy, ex.WorkerRouterPolicy)
		}
	case ex.WorkerToolsEnabled != nil:
		if !st.workerCalled || st.workerToolsEnabled == nil {
			return fmt.Errorf("worker_tools_enabled: worker was not called")
		}
		if *st.workerToolsEnabled != *ex.WorkerToolsEnabled {
			return fmt.Errorf("worker_tools_enabled = %v, want %v", *st.workerToolsEnabled, *ex.WorkerToolsEnabled)
		}
	case ex.WorkerCalls != nil:
		if st.persona.WorkerCalls != *ex.WorkerCalls {
			return fmt.Errorf("worker_calls = %d, want %d", st.persona.WorkerCalls, *ex.WorkerCalls)
		}
	case ex.RouteKind != "":
		if st.route == nil {
			return fmt.Errorf("route_kind: no OnRoute this turn")
		}
		if string(st.route.Kind) != ex.RouteKind {
			return fmt.Errorf("route_kind = %q, want %q", st.route.Kind, ex.RouteKind)
		}
	case ex.RouteSource != "":
		if st.route == nil {
			return fmt.Errorf("route_source: no OnRoute this turn")
		}
		if st.route.Source != ex.RouteSource {
			return fmt.Errorf("route_source = %q, want %q", st.route.Source, ex.RouteSource)
		}
	case ex.Metric != "":
		return fmt.Errorf("metric %q belongs on the scenario expect list", ex.Metric)
	case ex.PersonaID != "":
		if st.persona.Selection.PersonaID != ex.PersonaID {
			return fmt.Errorf("persona_id = %q, want %q", st.persona.Selection.PersonaID, ex.PersonaID)
		}
	case ex.DisplayName != "":
		if st.persona.Selection.DisplayName != ex.DisplayName {
			return fmt.Errorf("display_name = %q, want %q", st.persona.Selection.DisplayName, ex.DisplayName)
		}
	case ex.ResponsePersonaID != "":
		if st.persona.Envelope.PersonaID != ex.ResponsePersonaID {
			return fmt.Errorf("response_persona_id = %q, want %q", st.persona.Envelope.PersonaID, ex.ResponsePersonaID)
		}
	case ex.ResponseID != "":
		if st.persona.Envelope.ResponseID == "" || (ex.ResponseID != "*" && st.persona.Envelope.ResponseID != ex.ResponseID) {
			return fmt.Errorf("response_id = %q, want %q", st.persona.Envelope.ResponseID, ex.ResponseID)
		}
	case ex.ResponseVoice != "":
		if st.persona.Envelope.Voice != ex.ResponseVoice {
			return fmt.Errorf("response_voice = %q, want %q", st.persona.Envelope.Voice, ex.ResponseVoice)
		}
	case ex.ResponseBrainModel != "":
		if st.persona.Selection.BrainModel != ex.ResponseBrainModel {
			return fmt.Errorf("response_brain_model = %q, want %q", st.persona.Selection.BrainModel, ex.ResponseBrainModel)
		}
	case ex.SessionID != "":
		if st.persona.Selection.SessionID == "" || (ex.SessionID != "*" && st.persona.Selection.SessionID != ex.SessionID) {
			return fmt.Errorf("session_id = %q, want %q", st.persona.Selection.SessionID, ex.SessionID)
		}
	case ex.NoLiveBrainCall:
		if st.persona.BrainCalls != 0 {
			return fmt.Errorf("live brain calls = %d, want 0", st.persona.BrainCalls)
		}
	case ex.BrainCalls != nil:
		if st.persona.BrainCalls != *ex.BrainCalls {
			return fmt.Errorf("brain calls = %d, want %d", st.persona.BrainCalls, *ex.BrainCalls)
		}
	case ex.CanonicalIncludes != "":
		if !historyContains(st.persona.History, ex.CanonicalIncludes) {
			return fmt.Errorf("canonical history missing %q", ex.CanonicalIncludes)
		}
	case ex.WorkOriginPersona != "":
		if got := workOriginPersona(st.persona.Work); got != ex.WorkOriginPersona {
			return fmt.Errorf("work_origin_persona = %q, want %q", got, ex.WorkOriginPersona)
		}
	case ex.WorkCurrentResponder != "":
		if st.persona.Selection.PersonaID != ex.WorkCurrentResponder {
			return fmt.Errorf("work_current_responder = %q, want %q", st.persona.Selection.PersonaID, ex.WorkCurrentResponder)
		}
	case ex.WorkState != "":
		if got := workState(st.persona.Work); got != ex.WorkState {
			return fmt.Errorf("work_state = %q, want %q", got, ex.WorkState)
		}
	case ex.WorkPending != nil:
		if got := len(st.persona.Work.Pending) > 0; got != *ex.WorkPending {
			return fmt.Errorf("work_pending = %v, want %v", got, *ex.WorkPending)
		}
	case ex.WorkCommunicated != nil:
		if got := len(st.persona.Work.Communicated) > 0; got != *ex.WorkCommunicated {
			return fmt.Errorf("work_communicated = %v, want %v", got, *ex.WorkCommunicated)
		}
	case ex.WorkReceipt != nil:
		if got := workHasReceipt(st.persona.Work); got != *ex.WorkReceipt {
			return fmt.Errorf("work_receipt = %v, want %v", got, *ex.WorkReceipt)
		}
	case ex.WorkPersisted != nil:
		if got := st.persona.Work.Revision <= st.persona.Work.PersistedRevision; got != *ex.WorkPersisted {
			return fmt.Errorf("work_persisted = %v, want %v", got, *ex.WorkPersisted)
		}
	case ex.WorkRunningAtSelection != nil:
		atSelection := st.persona.WorkAtSelection
		if !atSelection.Scope.Valid() {
			atSelection = st.persona.Work
		}
		if got := workHasRunningJob(atSelection); got != *ex.WorkRunningAtSelection {
			return fmt.Errorf("work_running_at_selection = %v, want %v", got, *ex.WorkRunningAtSelection)
		}
	case ex.WorkCompletedBeforeSelection != nil:
		atSelection := st.persona.WorkAtSelection
		if !atSelection.Scope.Valid() {
			atSelection = st.persona.Work
		}
		if got := !workHasRunningJob(atSelection) && workHasTerminalJob(atSelection); got != *ex.WorkCompletedBeforeSelection {
			return fmt.Errorf("work_completed_before_selection = %v, want %v", got, *ex.WorkCompletedBeforeSelection)
		}
	}
	return nil
}

func workOriginPersona(snapshot work.Snapshot) string {
	for _, result := range append(append([]work.ResultView(nil), snapshot.Pending...), snapshot.Communicated...) {
		if persona := result.Outcome.Config["persona"]; persona != "" {
			return persona
		}
		if result.Outcome.Origin != "" {
			return result.Outcome.Origin
		}
	}
	return ""
}

func workState(snapshot work.Snapshot) string {
	if len(snapshot.Communicated) > 0 {
		return "communicated"
	}
	if len(snapshot.Pending) > 0 {
		return "pending"
	}
	if workHasRunningJob(snapshot) {
		return "running"
	}
	return ""
}

func workHasReceipt(snapshot work.Snapshot) bool {
	for _, result := range append(append([]work.ResultView(nil), snapshot.Pending...), snapshot.Communicated...) {
		if result.LastAttempt != nil && voiceReceiptComplete(result.LastAttempt.Receipt) {
			return true
		}
	}
	return false
}

func voiceReceiptComplete(receipt work.DeliveryReceipt) bool {
	return receipt.Mode == "voice" && receipt.ResponseCommitted && receipt.PlaybackComplete && receipt.Audible && !receipt.Interrupted && receipt.Segments > 0
}

func workHasRunningJob(snapshot work.Snapshot) bool {
	for _, job := range snapshot.Jobs {
		if job.State == work.JobRunning {
			return true
		}
	}
	return false
}

func workHasTerminalJob(snapshot work.Snapshot) bool {
	for _, job := range snapshot.Jobs {
		if job.State == work.JobTerminal {
			return true
		}
	}
	return false
}

type runMetrics struct {
	JobStarted         int
	JobFinished        int
	JobFailed          int
	JobAborted         int
	LiveToolCalls      int
	WorkerToolCalls    int
	BoundaryRefusals   int
	LiveRouterHit      int
	LiveRouterMiss     int
	SpokenLeakLines    int
	CrackleFailingWavs int
	PlaybackWavs       int
	crackleKnown       bool
}

// collectMetrics reads the whole run. A ToolCallStarted carrying a job id is
// a spawned worker's tool and counts as worker_tool_calls; one named "route"
// is the live router deciding, not a live tool; every other name is a tool
// the talking brain ran itself. live_router_miss is set when the run never
// routed or spawned at all — informational for the soft live-router
// scenario, never a gate on its own.
func collectMetrics(rec *recorder) runMetrics {
	var m runMetrics
	if rec == nil {
		return m
	}
	canonicalStates := make(map[work.JobKey]map[work.JobState]bool)
	for _, te := range rec.snapshot() {
		switch e := te.event.(type) {
		case events.WorkStateChanged:
			for _, job := range e.Snapshot.Jobs {
				key := job.Key
				seen := canonicalStates[key]
				if seen == nil {
					seen = make(map[work.JobState]bool)
					canonicalStates[key] = seen
				}
				if !seen[job.State] {
					switch {
					case job.State == work.JobRunning:
						m.JobStarted++
					case job.State == work.JobTerminal:
						switch canonicalOutcomeKind(job.Outcome) {
						case "finished":
							m.JobFinished++
						case "failed":
							m.JobFailed++
						case "aborted":
							m.JobAborted++
						}
					}
				}
				seen[job.State] = true
			}
		case events.JobStarted:
			m.JobStarted++
		case events.JobFinished:
			m.JobFinished++
		case events.JobFailed:
			m.JobFailed++
		case events.JobAborted:
			m.JobAborted++
		case events.ToolCallStarted:
			switch {
			case e.JobID != "":
				m.WorkerToolCalls++
			case e.Name == "route":
				m.LiveRouterHit++
			default:
				m.LiveToolCalls++
			}
		case events.ToolCallFinished:
			if e.JobID != "" && strings.Contains(e.Err, "work boundary") {
				m.BoundaryRefusals++
			}
		}
	}
	if m.LiveRouterHit == 0 && m.JobStarted == 0 {
		m.LiveRouterMiss = 1
	}
	m.SpokenLeakLines = leakLineCount(rec.spokenText())
	return m
}

func leakLineCount(spoken string) int {
	n := 0
	for _, line := range strings.Split(spoken, "\n") {
		if toolLeakLine(line) {
			n++
		}
	}
	return n
}

func toolLeakLine(line string) bool {
	s := strings.ToLower(line)
	if strings.Contains(s, "called the ") && strings.Contains(s, " tool") {
		return true
	}
	if strings.Contains(s, "```") {
		return true
	}
	return false
}

func evalRunExpect(ex Expectation, rec *recorder, m runMetrics) error {
	if ex.Metric == "" {
		st := turnState{spoken: rec.spokenText(), events: rec.snapshot()}
		if d, ok := rec.lastRoute(); ok {
			st.route = &d
		}
		return evalExpect(ex, rec, st)
	}
	if ex.Metric == "crackle_failing_wavs" {
		if !m.crackleKnown {
			return fmt.Errorf("metric crackle_failing_wavs is not measured until playback WAVs are scored")
		}
		if !compareMetric(m.CrackleFailingWavs, ex.Op, ex.Value) {
			return fmt.Errorf("metric %s: got %d, want %s %d", ex.Metric, m.CrackleFailingWavs, ex.Op, ex.Value)
		}
		return nil
	}
	got, ok := metricValue(m, ex.Metric)
	if !ok {
		return fmt.Errorf("unknown metric %q", ex.Metric)
	}
	if !compareMetric(got, ex.Op, ex.Value) {
		return fmt.Errorf("metric %s: got %d, want %s %d", ex.Metric, got, ex.Op, ex.Value)
	}
	return nil
}

func metricValue(m runMetrics, name string) (int, bool) {
	switch name {
	case "job_started":
		return m.JobStarted, true
	case "job_finished":
		return m.JobFinished, true
	case "job_failed":
		return m.JobFailed, true
	case "job_aborted":
		return m.JobAborted, true
	case "live_tool_calls":
		return m.LiveToolCalls, true
	case "worker_tool_calls":
		return m.WorkerToolCalls, true
	case "boundary_refusals":
		return m.BoundaryRefusals, true
	case "live_router_hit":
		return m.LiveRouterHit, true
	case "live_router_miss":
		return m.LiveRouterMiss, true
	case "spoken_leak_lines":
		return m.SpokenLeakLines, true
	default:
		return 0, false
	}
}

func compareMetric(got int, op string, want int) bool {
	switch op {
	case "==", "":
		return got == want
	case ">=":
		return got >= want
	case "<=":
		return got <= want
	case ">":
		return got > want
	case "<":
		return got < want
	default:
		return false
	}
}

// hasNamedEvent looks in the turn's own slice of the log. A nil turnLog
// means a run-level expect, which reads the whole run. There is no fallback
// from a turn to the whole run: `no_event: job_started` on a follow-up turn
// is exactly the assertion that the earlier spawn's JobStarted does not
// belong to this turn, and a positive `event:` that only the previous turn
// satisfied would be a false pass.
func hasCorrection(turnLog []timedEvent, rec *recorder, state string) bool {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	for _, te := range src {
		if c, ok := te.event.(events.JobCorrection); ok && c.State == state {
			return true
		}
	}
	return false
}

// checkJobControl wants the turn's job.control settlement to be want:
// "applied" or "refused", optionally ":<outcome or reason>", or "none".
func checkJobControl(turnLog []timedEvent, rec *recorder, want string) error {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	var settled []events.RouteDecided
	for _, te := range src {
		if r, ok := te.event.(events.RouteDecided); ok && r.Kind == string(pipeline.RouteJobControl) && r.State != "handled" {
			settled = append(settled, r)
		}
	}
	if want == "none" {
		if len(settled) > 0 {
			return fmt.Errorf("job_control: want no settlement, got %s %s", settled[0].State, settled[0].Message)
		}
		return nil
	}
	state, message, detailed := strings.Cut(want, ":")
	if len(settled) != 1 {
		return fmt.Errorf("job_control: want one settlement, got %d", len(settled))
	}
	if got := settled[0]; got.State != state || detailed && got.Message != message {
		return fmt.Errorf("job_control = %s:%s, want %s", got.State, got.Message, want)
	}
	return nil
}

// checkJobControlAction wants the turn's one applied job control to be want,
// told by what it did to the job it named.
func checkJobControlAction(turnLog []timedEvent, rec *recorder, want string) error {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	var settled []events.RouteDecided
	for _, te := range src {
		if r, ok := te.event.(events.RouteDecided); ok && r.Kind == string(pipeline.RouteJobControl) && r.State != "handled" {
			settled = append(settled, r)
		}
	}
	if len(settled) != 1 || settled[0].State != "applied" || settled[0].JobID == "" {
		return fmt.Errorf("job_control_action: want one applied settlement naming a job, got %+v", settled)
	}
	id := settled[0].JobID
	did := map[string]bool{}
	for _, te := range src {
		switch e := te.event.(type) {
		case events.JobAborted:
			if e.ID == id && e.Reason == events.AbortCancelled {
				did["cancel"] = true
			}
		case events.JobReport:
			if e.ID == id {
				did["report"] = true
			}
		case events.JobCorrection:
			if e.ID == id {
				did["steer"] = true
			}
		}
	}
	if !did[want] || len(did) != 1 {
		return fmt.Errorf("job_control_action: want %s on %s, the turn shows %v", want, id, did)
	}
	return nil
}

// spokenIn is what the log's speech events said, one line per sentence.
func spokenIn(log []timedEvent) string {
	var lines []string
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text != "" && (len(lines) == 0 || lines[len(lines)-1] != text) {
			lines = append(lines, text)
		}
	}
	for _, te := range log {
		switch e := te.event.(type) {
		case events.SpeechSegmentReady:
			add(e.Text)
		case events.GeneratingVoice:
			add(e.Sentence)
		}
	}
	return strings.Join(lines, " ")
}

func hasAbortReason(turnLog []timedEvent, rec *recorder, reason string) bool {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	for _, te := range src {
		if a, ok := te.event.(events.JobAborted); ok && a.Reason == reason {
			return true
		}
	}
	return false
}

func hasCutShort(turnLog []timedEvent, rec *recorder, reason string) bool {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	for _, te := range src {
		if f, ok := te.event.(events.JobFinished); ok && f.CutShort && f.CutShortReason == reason && strings.TrimSpace(f.Summary) != "" {
			return true
		}
	}
	return false
}

// toolsAfterReport counts the tool calls of the first reported job that
// started after its job_report event, or -1 when there is no report.
func toolsAfterReport(turnLog []timedEvent, rec *recorder) int {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	id, late := "", -1
	for _, te := range src {
		switch e := te.event.(type) {
		case events.JobReport:
			if id == "" {
				id, late = e.ID, 0
			}
		case events.ToolCallStarted:
			if id != "" && e.JobID == id {
				late++
			}
		}
	}
	return late
}

// checkWorkFile finds name at any depth under dir; a worker may write into a
// folder of its own there.
func checkWorkFile(dir, name, contains string) error {
	if dir == "" {
		return fmt.Errorf("work_file %q: the run has no work directory", name)
	}
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name {
			found = append(found, path)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("work_file %q: %w", name, err)
	}
	if len(found) == 0 {
		return fmt.Errorf("work_file %q: not under %s", name, dir)
	}
	for _, path := range found {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("work_file %q: %w", name, err)
		}
		if strings.Contains(string(body), contains) {
			return nil
		}
	}
	return fmt.Errorf("work_file %q: no copy holds %q", name, contains)
}

func hasNamedEvent(turnLog []timedEvent, rec *recorder, name string) bool {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	for _, te := range src {
		if eventMatchesName(te.event, name) {
			return true
		}
	}
	return false
}

func onlyRouteToolCalls(turnLog []timedEvent, rec *recorder) bool {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	saw := false
	for _, te := range src {
		tc, ok := te.event.(events.ToolCallStarted)
		if !ok {
			continue
		}
		saw = true
		if tc.Name != "route" {
			return false
		}
	}
	return saw
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
