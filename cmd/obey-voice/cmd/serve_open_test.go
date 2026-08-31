//go:build !integration

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
)

func TestStageServeURL(t *testing.T) {
	got := stageServeURL("https://127.0.0.1:7262/", &netapi.Credentials{
		Pairing: &netapi.PairingCode{Code: "123456", ExpiresAt: time.Now()},
	})
	want := "https://127.0.0.1:7262/stage.html?code=123456"
	if got != want {
		t.Fatalf("stageServeURL = %q, want %q", got, want)
	}
	if stageServeURL("https://127.0.0.1:7262/", nil) != "" {
		t.Fatal("expected empty URL without pairing")
	}
}

func TestApplyServeOpenDefaultsMutesHostSpeaker(t *testing.T) {
	origOpen, origVoice := serveOpen, serveNoVoice
	t.Cleanup(func() {
		serveOpen = origOpen
		serveNoVoice = origVoice
	})

	cmd := &cobra.Command{Use: "serve"}
	var nv bool
	cmd.Flags().BoolVar(&nv, "no-voice", false, "")
	serveOpen = true
	serveNoVoice = false
	applyServeOpenDefaults(cmd)
	if !serveNoVoice {
		t.Fatal("--open should mute the host speaker so voice plays once, in Stage")
	}
}

func TestOpenStageCallsOpenURL(t *testing.T) {
	var human bytes.Buffer
	origHuman, origOpen := serveHumanOut, openURL
	serveHumanOut = &human
	t.Cleanup(func() {
		serveHumanOut = origHuman
		openURL = origOpen
	})

	var opened string
	openURL = func(u string) error {
		opened = u
		return nil
	}

	origHost, origPort := servePublicHost, servePort
	servePublicHost = ""
	servePort = 7262
	t.Cleanup(func() {
		servePublicHost = origHost
		servePort = origPort
	})

	openStage("127.0.0.1:7262", &netapi.Credentials{
		Pairing: &netapi.PairingCode{Code: "654321"},
	})
	if opened != "https://127.0.0.1:7262/stage.html?code=654321" {
		t.Fatalf("opened %q", opened)
	}
	if !strings.Contains(human.String(), "Opened Stage") {
		t.Fatalf("human output = %q", human.String())
	}
}
