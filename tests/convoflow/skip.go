package main

import (
	"os/exec"
	"strings"
)

const exitSkip = 2

type knobs struct {
	liveProvider     string
	liveModel        string
	workerProvider   string
	workerModel      string
	workerOllamaHost string
	ollamaHost       string
	gate             bool
}

func skipReason(k knobs) string {
	if k.gate && strings.EqualFold(k.workerProvider, "stub") {
		return "gate refuses stub worker; pass worker_provider=ollama"
	}
	if strings.TrimSpace(k.liveModel) == "" && (strings.EqualFold(k.liveProvider, "ollama") || k.liveProvider == "") {
		return "live_model is empty"
	}
	if strings.EqualFold(k.workerProvider, "grok") {
		if _, err := exec.LookPath("grok"); err != nil {
			return "grok CLI not on PATH; use worker_provider=ollama for the local gate"
		}
	}
	if strings.EqualFold(k.workerProvider, "claude") {
		if _, err := exec.LookPath("claude"); err != nil {
			return "claude CLI not on PATH; use worker_provider=ollama for the local gate"
		}
	}
	return ""
}
