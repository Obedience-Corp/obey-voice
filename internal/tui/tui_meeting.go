package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
)

func (a App) onStartMeeting(msg startMeetingMsg) (tea.Model, tea.Cmd) {
	a.meetingRoutePlan = meetingRoutePlan{
		Kind: msg.RoutePlan,
		Dest: msg.Destination,
	}
	a.meetingRouteAuto = false
	a.meetingRouteBanner = ""
	a.meetingRouteErr = false
	a.meetingAnalysisBusy = false
	a.screen = screenMeeting
	a.meeting = newEmbeddedMeeting()
	a.meeting.width, a.meeting.height = a.width, a.height
	a.meeting.reflow()
	// Same wait, same screen as a conversation start: models load and the
	// mic opens before anything can be recorded.
	tick := a.meeting.beginStartup(msg.Description)
	return a, tea.Batch(tick, a.progress.wait(),
		buildMeeting(a.meetingBuilder, a.runCtx, a.progress, a.meetingSlot, MeetingSpec{
			Description: msg.Description,
			ResumePath:  msg.ResumePath,
		}))
}

func (a App) onMeetingReady(msg meetingReadyMsg) (tea.Model, tea.Cmd) {
	// Build finished after quit: the slot already owns the recorder's
	// teardown (or run() will call it), and applying it now would start a
	// listen loop on a program that is exiting.
	//
	// Only a.quitting, not runCtx: a cancelled run context is also how a
	// recorder is told its listen loop must exit, which is not the same
	// thing as the app going away.
	if a.quitting {
		return a, nil
	}
	if msg.err != nil {
		a.meeting.finishStartup()
		a.screen = screenMeetingSetup
		a.meetingSetup = newMeetingSetup(a.cfg)
		a.meetingSetup.width, a.meetingSetup.height = a.width, a.height
		a.meetingSetup.err = msg.err.Error()
		return a, nil
	}
	a.meetingRT = msg.rt
	// Persist the start-of-meeting delivery intent so it survives quits and
	// crashes (the startup sweep retries route_plan without routed): an
	// explicit dest pick, or mode=auto's default when no pick was made. A
	// failed write must not block recording, but it voids the durability
	// promise — surface it in the recorder timeline (R3).
	var planErr error
	plannedDest := ""
	if msg.rt.Writer != nil {
		switch {
		case a.meetingRoutePlan.Kind == routePlanDest && a.meetingRoutePlan.Dest.ID != "":
			plannedDest = a.meetingRoutePlan.Dest.ID
			planErr = msg.rt.Writer.WriteRoutePlan(plannedDest, routePlanDest)
		case a.meetingRoutePlan.Kind == "" && a.cfg != nil:
			if rc := meeting.FromConfig(a.cfg); rc.Mode == meeting.ModeAuto && rc.Default != "" {
				plannedDest = rc.Default
				planErr = msg.rt.Writer.WriteRoutePlan(plannedDest, "auto")
			}
		}
	}
	speakerStatus, speakerError := msg.rt.SpeakerStatus, msg.rt.SpeakerError
	if demoMeetingSpeakersEnabled() && msg.rt.FinalizeSpeakers == nil {
		// Background diarize runs off the runtime; the demo finalizer must
		// land there to run at all.
		msg.rt.FinalizeSpeakers = demoMeetingSpeakerFinalizer(msg.rt.Writer, msg.rt.Path)
		speakerStatus = meeting.AnalysisQueued
		speakerError = "scripted multi-speaker fixture"
	}
	// Child cancel stops the listen loop without ending the whole App.
	mctx, mcancel := context.WithCancel(a.runCtx)
	a.meeting.finishStartup()
	cmd := a.meeting.beginRecording(MeetingOpts{
		Ctx:           mctx,
		Cancel:        mcancel,
		Capture:       msg.rt.Capture,
		Provider:      msg.rt.Provider,
		Writer:        msg.rt.Writer,
		Description:   msg.rt.Description,
		Path:          msg.rt.Path,
		StopPhrases:   msg.rt.StopPhrases,
		SpeakerStatus: speakerStatus,
		SpeakerError:  speakerError,
		LiveSpeaker:   msg.rt.LiveSpeaker,
		Embedded:      true,
	})
	if planErr != nil {
		a.meeting.appendSystemLine(errorStyle.Render(fmt.Sprintf(
			"  ⚠ route intent not saved (%v) — %s delivery depends on this session; crash recovery unavailable",
			planErr, plannedDest)))
	}
	return a, cmd
}

func (a App) onMeetingDone(msg meetingDoneMsg) (tea.Model, tea.Cmd) {
	rt, summary, closeErr := a.finishMeetingCapture()
	plan := a.meetingRoutePlan
	if err := errors.Join(msg.Err, closeErr); err != nil {
		if rt != nil && rt.Cleanup != nil {
			rt.Cleanup()
		}
		banner := fmt.Sprintf("Meeting ended with error: %v", err)
		// Fail loudly when a chosen destination was not reached (R3): the
		// startup sweep cannot retry a bundle whose trailer never landed.
		if plan.Kind == routePlanDest && plan.Dest.ID != "" {
			a.meetingRoutePlan = meetingRoutePlan{}
			banner += fmt.Sprintf(" — NOT filed to %s; recover with: obey-voice meeting route %s --to %s",
				plan.Dest.ID, summary.Bundle, plan.Dest.ID)
		}
		a.screen = screenLauncher
		a.launcher = a.launcher.withBanner(banner, true)
		return a, nil
	}
	// Keep the completed meeting visible for review. Capture stop is the end
	// of the meeting: diarization and a start-plan route continue behind the
	// results screen instead of gating it (WI-162bbb R1/R2).
	a.meetingResults = newMeetingResults(summary)
	a.meetingResults.width, a.meetingResults.height = a.width, a.height
	var cmds []tea.Cmd
	if rt != nil && rt.FinalizeSpeakers != nil {
		a.meetingAnalysisBusy = true
		a.meetingResults.analysisBusy = true
		cmds = append(cmds, runBackgroundDiarize(a.runCtx, rt))
	} else if rt != nil && rt.Cleanup != nil {
		rt.Cleanup()
	}
	if plan.Kind == routePlanDest && plan.Dest.ID != "" && a.cfg != nil {
		a.meetingRoutePlan = meetingRoutePlan{} // consume once
		a.meetingRouteAuto = true
		a.meetingRouteBanner = ""
		a.meetingRouteErr = false
		a.meetingResults.routeStatus = "Filing to " + plan.Dest.ID + "…"
		cmds = append(cmds, destRouteCmd(summary, meeting.FromConfig(a.cfg), plan.Dest))
	}
	a.meetingResults.resize()
	a.screen = screenMeetingResults
	return a, tea.Batch(cmds...)
}

func (a App) onMeetingResultsDone(msg meetingResultsDoneMsg) (tea.Model, tea.Cmd) {
	// This meeting is the new resume candidate.
	a.launcher = a.launcher.forgetResumeCandidate()
	// A dest plan already routed at capture end; leaving results just needs
	// the launcher to carry the outcome (or the still-pending note).
	if a.meetingRouteAuto {
		banner, isErr := a.meetingRouteBanner, a.meetingRouteErr
		if banner == "" {
			banner, isErr = "Filing meeting notes…", false
		}
		a.screen = screenLauncher
		a.launcher = a.launcher.withBanner(banner, isErr)
		return a, nil
	}
	// Post-meeting routing: ask / auto / off / start-picker plan.
	// beginMeetingRoute may switch screens or return asynchronous work.
	prevScreen := a.screen
	if cmd := a.beginMeetingRoute(msg.summary); cmd != nil {
		if a.screen == prevScreen {
			a.screen = screenLauncher
			a.launcher = a.launcher.withBanner("Routing meeting notes…", false)
		}
		return a, cmd
	}
	if a.screen != prevScreen {
		return a, nil
	}
	a.screen = screenLauncher
	a.launcher = a.launcher.withBanner("Meeting saved: "+msg.summary.Bundle, false)
	return a, nil
}

func (a App) onMeetingRouteReady(msg meetingRouteReadyMsg) (tea.Model, tea.Cmd) {
	a.applyMeetingRouteReady(msg)
	return a, nil
}

func (a App) onMeetingRouteResult(msg meetingRouteResultMsg) (tea.Model, tea.Cmd) {
	a.meetingRouteBanner = msg.Banner
	a.meetingRouteErr = msg.IsErr
	// The resume candidate's route status just moved.
	a.launcher = a.launcher.forgetResumeCandidate()
	// Route finished while the user is still reviewing: show it on the
	// results status line and keep them there; the launcher banner lands
	// when they leave (meetingResultsDoneMsg).
	if a.screen == screenMeetingResults {
		a.meetingResults.routeStatus = msg.Banner
		a.meetingResults.routeErr = msg.IsErr
		return a, nil
	}
	a.screen = screenLauncher
	if msg.Banner != "" {
		a.launcher = a.launcher.withBanner(msg.Banner, msg.IsErr)
	}
	return a, nil
}

func (a App) onMeetingAnalysisDone(msg meetingAnalysisDoneMsg) (tea.Model, tea.Cmd) {
	a.meetingAnalysisBusy = false
	if a.screen == screenMeetingResults {
		a.meetingResults.applyAnalysis(msg.result, msg.err)
		return a, nil
	}
	// R3: background completion stays loud after leaving results, but only
	// the launcher shows banners — never interrupt a live conversation.
	if a.screen == screenLauncher {
		if failed, detail := analysisFailure(msg.result, msg.err); failed {
			a.launcher = a.launcher.withBanner("Speaker analysis failed: "+detail, true)
		} else {
			a.launcher = a.launcher.withBanner("Speaker labels updated: "+meetingAnalysisDetail(msg.result), false)
		}
	}
	return a, nil
}

func (a App) onMeetingSweepDone(msg meetingSweepDoneMsg) (tea.Model, tea.Cmd) {
	if banner, isErr := sweepBanner(msg.results); banner != "" && a.screen == screenLauncher {
		a.launcher = a.launcher.withBanner(banner, isErr)
	}
	return a, nil
}
