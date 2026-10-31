package tui

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

const demoWorkersEnv = "OBEY_VOICE_DEMO_WORKERS"
const demoLauncherNoticeEnv = "OBEY_VOICE_DEMO_LAUNCHER_NOTICE"

func demoLauncherNotices() []string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoLauncherNoticeEnv))) {
	case "route", "json", "1", "true", "yes", "on":
		return []string{
			`Meeting saved: /home/demo/.obey/agents/voice/obey-voice/meetings/test-20261002-121718.meeting`,
			`Meeting notes routed to camp:ExampleCampaign: {"schema_version":"intent-meeting-import/v1alpha1","campaign_root":"/home/demo/campaigns/ExampleCampaign","generated_at":"2026-10-02T18:17:48Z","note":{"duration_seconds":44270,"id":"test-20261002-121748","path":"meetings/test-20261002-121748.md","transcript":"transcripts/test-20261002-121748.txt","speakers":1,"utterances":1,"updated_existing":false}}`,
		}
	case "scroll":
		out := make([]string, 0, 13)
		for i := range 12 {
			out = append(out, "Meeting saved: /tmp/meetings/note-"+strconv.Itoa(i)+".meeting")
		}
		out = append(out, `Meeting notes routed to camp:ExampleCampaign: {"schema_version":"intent-meeting-import/v1alpha1","campaign_root":"/home/demo/campaigns/ExampleCampaign","generated_at":"2026-10-02T18:17:48Z","note":{"duration_seconds":44270,"id":"test-20261002-121748","path":"meetings/test-20261002-121748.md","transcript":"transcripts/test-20261002-121748.txt","speakers":1,"utterances":1,"updated_existing":false}}`)
		return out
	case "speakers", "path":
		return []string{"Speaker labels updated: 7 speakers · /home/demo/.obey/agents/voice/obey-voice/meetings/example-planning-20261002-121718.meeting/.samantha/speaker-analysis.json"}
	default:
		return nil
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
	task := "Now just tell me the weather in denver colorado"
	if demoWorkCueEnabled() {
		task = demoWorkCueTask
	}
	return &demoWorkerController{
		inner: inner,
		items: []pipeline.WorkerInfo{{
			ID:       "job-1",
			Task:     task,
			State:    "running",
			Provider: "ollama",
			Model:    "orcarouter/Qwen3.8-27B-Uncensored:q4_K_M",
			Activity: activity,
			Elapsed:  39 * time.Second,
		}},
	}
}
