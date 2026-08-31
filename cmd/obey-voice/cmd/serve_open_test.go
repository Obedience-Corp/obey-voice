//go:build !integration

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
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

	openStage("192.168.0.188:7262", &netapi.Credentials{
		Pairing: &netapi.PairingCode{Code: "654321"},
	})
	if opened != "http://127.0.0.1:7262/stage.html?code=654321" {
		t.Fatalf("opened LAN bind, want loopback HTTP, got %q", opened)
	}
	if !strings.Contains(human.String(), "Opened Stage") {
		t.Fatalf("human output = %q", human.String())
	}
}

func TestLocalStageURLIsHTTPWhenOpen(t *testing.T) {
	origOpen, origHost, origPort := serveOpen, servePublicHost, servePort
	t.Cleanup(func() {
		serveOpen, servePublicHost, servePort = origOpen, origHost, origPort
	})
	serveOpen = true
	servePublicHost = ""
	servePort = 7262

	got := localStagePageURL("192.168.0.188:7262")
	if got != "http://127.0.0.1:7262/" {
		t.Fatalf("localStagePageURL = %q, want loopback HTTP", got)
	}
	stage := stageServeURL(got, &netapi.Credentials{
		Pairing: &netapi.PairingCode{Code: "123456"},
	})
	if stage != "http://127.0.0.1:7262/stage.html?code=123456" {
		t.Fatalf("advertised Stage URL = %q", stage)
	}
}

func TestListenAdvertiseURLLoopbackHTTPWhenOpen(t *testing.T) {
	orig := serveOpen
	t.Cleanup(func() { serveOpen = orig })

	serveOpen = true
	if got := listenAdvertiseURL("127.0.0.1:7262"); got != "http://127.0.0.1:7262" {
		t.Fatalf("loopback --open Listening = %q", got)
	}
	if got := listenAdvertiseURL("192.168.0.188:7262"); got != "https://192.168.0.188:7262" {
		t.Fatalf("LAN --open Listening = %q, want HTTPS", got)
	}

	serveOpen = false
	if got := listenAdvertiseURL("127.0.0.1:7262"); got != "https://127.0.0.1:7262" {
		t.Fatalf("loopback without --open Listening = %q, want HTTPS", got)
	}
}

func TestBannerDoesNotAdvertiseHTTPSLoopbackWhenOpen(t *testing.T) {
	var human bytes.Buffer
	origHuman, origOpen, origHost, origPort := serveHumanOut, serveOpen, servePublicHost, servePort
	serveHumanOut = &human
	serveOpen = true
	servePublicHost = ""
	servePort = 7262
	t.Cleanup(func() {
		serveHumanOut, serveOpen, servePublicHost, servePort = origHuman, origOpen, origHost, origPort
	})

	creds := &netapi.Credentials{
		Pairing: &netapi.PairingCode{Code: "123456", ExpiresAt: time.Now().Add(time.Minute)},
	}
	printServeBanner("192.168.0.188:7262", creds, &config.Config{})
	out := human.String()
	if strings.Contains(out, "https://127.0.0.1") {
		t.Fatalf("banner advertised HTTPS loopback:\n%s", out)
	}
	if !strings.Contains(out, "http://127.0.0.1:7262/stage.html?code=123456") {
		t.Fatalf("banner Stage URL missing loopback HTTP:\n%s", out)
	}
	if !strings.Contains(out, "https://192.168.0.188:7262") {
		t.Fatalf("LAN Listening should stay HTTPS:\n%s", out)
	}

	human.Reset()
	printServeBanner("127.0.0.1:7262", creds, &config.Config{})
	out = human.String()
	if strings.Contains(out, "https://127.0.0.1") {
		t.Fatalf("loopback bind advertised HTTPS:\n%s", out)
	}
	if !strings.Contains(out, "Listening:") || !strings.Contains(out, "http://127.0.0.1:7262") {
		t.Fatalf("loopback Listening should be HTTP:\n%s", out)
	}
}

func TestLocalStageURLKeepsMagicDNSHTTPS(t *testing.T) {
	origOpen, origHost, origPort := serveOpen, servePublicHost, servePort
	t.Cleanup(func() {
		serveOpen, servePublicHost, servePort = origOpen, origHost, origPort
	})
	serveOpen = true
	servePublicHost = "mac.tail.ts.net"
	servePort = 7262
	got := localStagePageURL("100.64.0.7:7262")
	if got != "https://mac.tail.ts.net:7262/" {
		t.Fatalf("Tailscale --open = %q, want MagicDNS HTTPS", got)
	}
}

func TestReadyBannerLoopbackHTTPWhenOpen(t *testing.T) {
	withServeBindState(t)
	orig := serveOpen
	serveOpen = true
	t.Cleanup(func() { serveOpen = orig })

	loop := captureReadyBanner(t, "127.0.0.1:7262", []string{"127.0.0.1:7262"})
	if loop.URL != "http://127.0.0.1:7262" {
		t.Fatalf("loopback ready url = %q, want http", loop.URL)
	}
	lan := captureReadyBanner(t, "192.168.0.188:7262", []string{"192.168.0.188:7262", "127.0.0.1:7262"})
	if lan.URL != "https://192.168.0.188:7262" {
		t.Fatalf("LAN ready url = %q, want https", lan.URL)
	}
	// ADR-008 binds stay host:port. Under --open the loopback extra is HTTP
	// even though url is HTTPS — clients must not reuse url's scheme.
	if lan.Binds[1] != "127.0.0.1:7262" {
		t.Fatalf("loopback bind = %q, want host:port without scheme", lan.Binds[1])
	}
}
