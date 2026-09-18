package tui

import (
	"os"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

const demoWorkersEnv = "OBEY_VOICE_DEMO_WORKERS"
const demoLauncherNoticeEnv = "OBEY_VOICE_DEMO_LAUNCHER_NOTICE"

func demoLauncherNotice() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoLauncherNoticeEnv))) {
	case "route", "json", "1", "true", "yes", "on":
		return `Meeting notes routed to camp:JobSearch: {"campaign_root":"/Users/lancerogers/Dev/JobSearch","generated_at":"2026-09-18T17:19:25.557803Z"}`
	case "speakers", "path":
		return "Speaker labels updated: 7 speakers · /Users/lancerogers/.obey/agents/voice/festival-voice/meetings/recruiter-call-eyong-20260918-110210.meeting/.samantha/speaker-analysis.json"
	default:
		return ""
	}
}

func demoWorkersEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoWorkersEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

type demoWorkerController struct {
	inner workerController
	items []pipeline.WorkerInfo
}

func (d *demoWorkerController) Workers() []pipeline.WorkerInfo {
	return append([]pipeline.WorkerInfo(nil), d.items...)
}

func (d *demoWorkerController) SetWorkersEnabled(enabled bool) {
	if d.inner != nil {
		d.inner.SetWorkersEnabled(enabled)
	}
}

func (d *demoWorkerController) CancelWork(id string) error {
	if d.inner != nil {
		return d.inner.CancelWork(id)
	}
	return nil
}

func (d *demoWorkerController) StopWorkers() error {
	if d.inner != nil {
		return d.inner.StopWorkers()
	}
	return nil
}

func demoWorkers(inner workerController) workerController {
	activity := `searched 1 time (1 unknown) · {"query":"Denver CO current temperature today","results":[{"title":"Denver, CO Current Weather | AccuWeather","url":"https://www.accuweather.com/en/us/denver/80203/current-weather/347810","snippet":"Current weather in Denver, CO. Check conditions, forecasts, and radar.","temperature_f":48,"wind_mph":9,"ok":true}]}`
	return &demoWorkerController{
		inner: inner,
		items: []pipeline.WorkerInfo{{
			ID:       "job-1",
			Task:     "Now just tell me the weather in denver colorado",
			State:    "running",
			Provider: "ollama",
			Model:    "orcarouter/Qwen3.8-27B-Uncensored:q4_K_M",
			Activity: activity,
			Elapsed:  39 * time.Second,
		}},
	}
}
