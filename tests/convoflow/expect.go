package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

type turnState struct {
	spoken             string
	events             []timedEvent
	route              *pipeline.RouteDecision
	history            []brain.Turn
	liveRouterPolicy   string
	workerRouterPolicy string
	workerToolsEnabled *bool
	liveBrainCalled    bool
	workerCalled       bool
}

func evalExpect(ex Expectation, rec *recorder, st turnState) error {
	switch {
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
	}
	return nil
}

type runMetrics struct {
	JobStarted         int
	JobFinished        int
	JobFailed          int
	LiveToolCalls      int
	LiveRouterHit      int
	LiveRouterMiss     int
	SpokenLeakLines    int
	CrackleFailingWavs int
	PlaybackWavs       int
	crackleKnown       bool
}

// collectMetrics reads the whole run. A ToolCallStarted named "route" is the
// live router deciding, not a live tool; every other name is a tool the
// talking brain ran itself. live_router_miss is set when the run never
// routed or spawned at all — informational for the soft live-router
// scenario, never a gate on its own.
func collectMetrics(rec *recorder) runMetrics {
	var m runMetrics
	if rec == nil {
		return m
	}
	for _, te := range rec.snapshot() {
		switch e := te.event.(type) {
		case events.JobStarted:
			m.JobStarted++
		case events.JobFinished:
			m.JobFinished++
		case events.JobFailed:
			m.JobFailed++
		case events.ToolCallStarted:
			if e.Name == "route" {
				m.LiveRouterHit++
			} else {
				m.LiveToolCalls++
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
	case "live_tool_calls":
		return m.LiveToolCalls, true
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
func hasNamedEvent(turnLog []timedEvent, rec *recorder, name string) bool {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	for _, te := range src {
		if eventName(te.event) == name {
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
