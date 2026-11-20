package brain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

func (o *OllamaBrain) finishStreamRoute(sess *toolSession, toolCalls []api.ToolCall, kind string, jsonRouting, inspectedLocally bool) StreamResult {
	tier, reason := routeMetadata(toolCalls)
	if !jsonRouting && (sess.onStart != nil || sess.onEnd != nil) {
		decision := sess.describe("route", kind, "")
		begun := time.Now()
		if sess.onStart != nil {
			sess.onStart(decision)
		}
		if sess.onEnd != nil {
			elapsed := time.Since(begun)
			sess.onEnd(ToolResult{ToolCall: decision, Preview: kind, Elapsed: elapsed, Outcome: &events.ToolOutcome{
				Status: events.ToolOutcomeSucceeded, InputSummary: kind, OutputPreview: kind, Elapsed: elapsed,
			}})
		}
	}
	// The routed history writer owns an untouched user line.
	// After a local inspection, keep its user/tool exchange
	// intact; the writer appends only the acknowledgment.
	if n := len(o.history); n > 0 && !inspectedLocally {
		if len(o.canonical) > 0 && len(o.working) > 0 {
			o.stopped.routed = &ollamaRoutedLine{canonical: o.canonical[len(o.canonical)-1], working: o.working[len(o.working)-1], message: o.history[n-1]}
		}
		o.history = o.history[:n-1]
		if len(o.canonical) > 0 {
			o.canonical = o.canonical[:len(o.canonical)-1]
		}
		if len(o.working) > 0 {
			o.working = o.working[:len(o.working)-1]
		}
	}
	return StreamResult{Route: kind, RouteTier: tier, RouteReasonCode: reason}
}

func (sess *toolSession) checkStreamRoute(ctx context.Context, routing, jsonRouting, complete bool, toolCalls []api.ToolCall, started time.Time, err error) error {
	var routeRejected error
	if jsonRouting && errors.Is(err, errRouteRejected) {
		routeRejected, err = err, nil
	}
	if err == nil && routing && !complete {
		err = fmt.Errorf("routing response ended before completion")
	}
	if jsonRouting && ctx.Err() == nil {
		failure := routeRejected
		if failure == nil {
			failure = err
		}
		sess.reportRoute(toolCalls, failure, time.Since(started))
	}
	return err
}
