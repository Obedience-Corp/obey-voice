package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type turnState struct {
	spoken             string
	events             []timedEvent
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
		d, ok := rec.lastRoute()
		if !ok {
			return fmt.Errorf("route_kind: no OnRoute")
		}
		if string(d.Kind) != ex.RouteKind {
			return fmt.Errorf("route_kind = %q, want %q", d.Kind, ex.RouteKind)
		}
	case ex.RouteSource != "":
		d, ok := rec.lastRoute()
		if !ok {
			return fmt.Errorf("route_source: no OnRoute")
		}
		if d.Source != ex.RouteSource {
			return fmt.Errorf("route_source = %q, want %q", d.Source, ex.RouteSource)
		}
	case ex.Metric != "":
		return fmt.Errorf("run-level metric expects are evaluated after the conversation")
	}
	return nil
}

func hasNamedEvent(turnLog []timedEvent, rec *recorder, name string) bool {
	src := turnLog
	if src == nil && rec != nil {
		src = rec.snapshot()
	}
	if name == "job_terminal" {
		for _, te := range src {
			if jobTerminal(te.event) {
				return true
			}
		}
		return rec != nil && rec.hasEvent("job_terminal")
	}
	for _, te := range src {
		if eventName(te.event) == name {
			return true
		}
	}
	if rec != nil && turnLog != nil {
		// job_started may have arrived before this turn's slice
		return rec.hasEvent(name)
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
