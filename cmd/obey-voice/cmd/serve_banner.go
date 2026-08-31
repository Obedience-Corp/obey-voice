//go:build !integration

package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// printServeBanner writes the human "Use on another device" card. Every line
// goes to serveHumanOut, never to stdout directly: under --banner-json stdout
// belongs to the JSON protocol, and under --log-file the card belongs in the
// file.
func printServeBanner(addr string, creds *netapi.Credentials, cfg *config.Config) {
	fmt.Fprintln(serveHumanOut)
	fmt.Fprintf(serveHumanOut, "  %s\n", titleStyle.Render("Use on another device"))

	pageURL := clientPageURL(addr)
	fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelOpenOnClient), pageURL)

	if serveTailscale {
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelNetwork), netapi.NetworkTailscale)
	} else {
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelNetwork), netapi.NetworkLAN)
	}

	printServeClientAccess(creds)
	printServePairing(creds)
	stagePage := pageURL
	if serveOpen {
		stagePage = localStagePageURL(addr)
	}
	printServeStage(stagePage, creds)
	printServeDetails(addr, creds, cfg)
}

// printServeClientAccess reports the product-facing access mode. The TUI parses
// these labels via netapi constants. Trusted certs → full voice mic in any
// browser; self-signed → the page works after a warning, and some mobile
// browsers still block the mic.
func printServeClientAccess(creds *netapi.Credentials) {
	if creds.ExternalTLS {
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelClientAccess), netapi.AccessFull)
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Any device on this network can use text and the microphone."))
		return
	}
	fmt.Fprintf(serveHumanOut, "  %s %s\n", failStyle.Render(netapi.LabelClientAccess), netapi.AccessLimited)
	if serveTailscale || servePublicHost != "" {
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelClientSetup), netapi.ClientSetupURL)
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Works now: most desktop browsers (accept one warning) + text on any device."))
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Full mic on every browser: Client setup → HTTPS Certificates → restart."))
		return
	}
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Works now: most desktop browsers (accept one warning) + text on any device."))
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Full mic on every browser: obey-voice serve --tailscale (with HTTPS"))
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Certificates on), or pass --tls-cert/--tls-key."))
}

func printServePairing(creds *netapi.Credentials) {
	if creds.Pairing == nil {
		return
	}
	fmt.Fprintf(serveHumanOut, "  %s %s  (expires %s)\n",
		keyStyle.Render(netapi.LabelPairingCode),
		creds.Pairing.Code,
		creds.Pairing.ExpiresAt.Format("15:04:05"))
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  On the device: open link → enter code → Pair → Start, or open Stage (already paired)."))
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  The code is single-use; restart serve to issue another."))
}

func clientPageURL(listenAddr string) string {
	pageHost := servePublicHost
	if pageHost == "" {
		if host, _, err := net.SplitHostPort(listenAddr); err == nil {
			pageHost = host
		} else {
			pageHost = listenAddr
		}
	}
	if serveOpen && isLoopbackHost(pageHost) {
		return loopbackHTTPBaseURL(listenAddr)
	}
	return publicServeURL(pageHost, servePort)
}

// localStagePageURL is the URL --open loads on this machine. Loopback HTTP
// skips the self-signed cert warning; MagicDNS / public host stay HTTPS.
func localStagePageURL(listenAddr string) string {
	if strings.TrimSpace(servePublicHost) != "" {
		return clientPageURL(listenAddr)
	}
	return loopbackHTTPBaseURL(listenAddr)
}

func loopbackHTTPBaseURL(listenAddr string) string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		port = strconv.Itoa(servePort)
	}
	return "http://127.0.0.1:" + port + "/"
}

// listenAdvertiseURL is the scheme:host:port printed for a bound address.
// --open / LoopbackHTTP is plain HTTP on loopback; LAN and Tailscale stay HTTPS.
func listenAdvertiseURL(addr string) string {
	return listenScheme(addr) + "://" + addr
}

func listenScheme(addr string) string {
	if serveOpen && isLoopbackListenAddr(addr) {
		return "http"
	}
	return "https"
}

func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return isLoopbackHost(addr)
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func stageServeURL(pageURL string, creds *netapi.Credentials) string {
	if creds == nil || creds.Pairing == nil || strings.TrimSpace(pageURL) == "" || creds.Pairing.Code == "" {
		return ""
	}
	return strings.TrimRight(pageURL, "/") + "/stage.html?code=" + creds.Pairing.Code
}

func printServeStage(pageURL string, creds *netapi.Credentials) {
	stage := stageServeURL(pageURL, creds)
	if stage == "" {
		return
	}
	fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelStage), stage)
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Camera face — already paired. Prefer: obey-voice serve --open"))
}

// printServeDetails writes the secondary / power-user lines, dim and last.
func printServeDetails(addr string, creds *netapi.Credentials, cfg *config.Config) {
	fmt.Fprintf(serveHumanOut, "  %s %s\n", dimStyle.Render("Listening:"), listenAdvertiseURL(addr))
	if creds.ExternalTLS {
		fp := creds.Fingerprint
		if len(fp) > 16 {
			fp = fp[:16] + "…"
		}
		fmt.Fprintf(serveHumanOut, "  %s %s\n", dimStyle.Render("TLS:"), "trusted ("+fp+")")
	} else {
		fmt.Fprintf(serveHumanOut, "  %s %s\n", dimStyle.Render("TLS:"), "browser warning (self-signed)")
		if serveTLSFallbackNote != "" {
			fmt.Fprintf(serveHumanOut, "  %s %s\n", dimStyle.Render("Detail:"), serveTLSFallbackNote)
		}
	}
	if creds.TokenCreated {
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render("Token:"), creds.Token)
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Shown once — stored under "+config.ConfigDir()+"/serve/"))
	} else {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Token on file at "+config.ConfigDir()+"/serve/token"))
	}
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Revoke: obey-voice serve --revoke-tokens"))
	printServeWorkDir()
	// Printed with the remote-tools line on purpose: together they say who may
	// trigger a tool and which folder it starts in.
	if cfg.RemoteToolsEnabled {
		fmt.Fprintln(serveHumanOut, failStyle.Render("  WARNING: remote_tools_enabled=true — network clients can trigger tool calls on this machine, starting in "+cfg.WorkDir))
	} else {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Remote tool calls: disabled (remote_tools_enabled=false); tools start in "+cfg.WorkDir))
	}
	if serveOpen {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Voice: plays in the Stage window (host speaker muted so it does not double)"))
	} else if serveNoVoice {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Host speaker: muted (--no-voice); stream clients can still request audio_output"))
	} else {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Host speaker: on; clients opt in with {\"type\":\"audio_output\",\"mode\":\"stream\"}"))
	}
	if serveOpen {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Stage mic: open — just talk; tap the orb to cut"))
	} else if serveRemoteMic {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Remote mic: push-to-talk enabled (hold Talk on the voice page)"))
	} else {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Remote mic: off (--remote-mic=false)"))
	}
	if serveTailscale {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Mode: --tailscale (shared Tailscale network; host speaker muted unless --no-voice=false)"))
	} else {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Mode: LAN / same network (mDNS when enabled)"))
	}
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Debug: obey-voice connect "+addr+" --token <token>"))
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Local full voice on this machine: run `obey-voice` (TUI), not serve"))
	fmt.Fprintln(serveHumanOut)
}

// printServeWorkDir states where the agent works, who decided that, and which
// campaign it lands in. A source of env or a fallback is not dim: nobody typed
// it, and it is the case where a serve quietly works in the wrong campaign.
func printServeWorkDir() {
	info := serveBannerWorkDir()
	line := fmt.Sprintf("  Work dir: %s (from %s)", info.Dir, info.Describe())
	switch info.Source {
	case config.WorkDirSourceEnv, config.WorkDirSourceHomeFallback, config.WorkDirSourceTempFallback:
		fmt.Fprintln(serveHumanOut, failStyle.Render(line))
	default:
		fmt.Fprintln(serveHumanOut, dimStyle.Render(line))
	}
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Campaign: "+config.CampaignFor(info.Dir).Describe()))
}

// emitServeBannerJSON writes the machine-readable ready line (and a
// pairing_code line when a code is minted) to stdout, one JSON object per
// line. The supervising process reads the ready line to learn the URL,
// credentials, and fingerprint instead of scraping the human banner.
func emitServeBannerJSON(listenAddr string, allBinds []string, creds *netapi.Credentials) {
	host, port := hostPortFromAddr(listenAddr)
	pageHost := servePublicHost
	if pageHost == "" {
		pageHost = host
	}

	workDir := serveBannerWorkDir()
	urlHost := net.JoinHostPort(pageHost, strconv.Itoa(port))
	emitBannerLine(netapi.ReadyBanner{
		Event:           "ready",
		ProtocolVersion: netapi.ProtocolVersion,
		URL:             listenScheme(urlHost) + "://" + urlHost,
		Port:            port,
		Fingerprint:     creds.Fingerprint,
		Token:           creds.Token,
		MDNS:            !serveNoMDNS,
		Tailscale:       serveTailscale,
		PID:             os.Getpid(),
		Binds:           allBinds,
		WorkDir:         workDir.Dir,
		WorkDirSource:   string(workDir.Source),
		Campaign:        bannerCampaign(workDir.Dir),
		ClientSetupURL:  serveClientSetupURL,
		Capabilities:    netapi.Capabilities(),
	})

	if creds.Pairing != nil {
		emitBannerLine(netapi.PairingCodeBanner{
			Event:     "pairing_code",
			Code:      creds.Pairing.Code,
			ExpiresAt: creds.Pairing.ExpiresAt.Format(time.RFC3339),
		})
	}
}

// bannerCampaign maps the detected campaign onto the wire type. netapi owns its
// own shape rather than exporting a config struct over the network.
func bannerCampaign(workDir string) netapi.CampaignInfo {
	c := config.CampaignFor(workDir)
	return netapi.CampaignInfo{Found: c.Found(), Name: c.Name, ID: c.ID, Root: c.Root}
}

// hostPortFromAddr splits a host:port listen address, falling back to the
// configured serve port when the address carries no parseable port.
func hostPortFromAddr(addr string) (host string, port int) {
	port = servePort
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, port
	}
	if n, err := strconv.Atoi(p); err == nil {
		port = n
	}
	return h, port
}

func emitBannerLine(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(serveDiagOut, "  warning: encode serve banner: %v\n", err)
		return
	}
	fmt.Fprintf(serveBannerOut, "%s\n", data)
}

// serveModelProgress mirrors modelProgress but writes to serveHumanOut so
// asset-download output never pollutes stdout under --banner-json.
var serveLastProgressPct int

func serveModelProgress(name string, pct float64) {
	iPct := int(pct)
	if pct == 0 {
		serveLastProgressPct = -1
		fmt.Fprintf(serveHumanOut, "  Downloading %s...\n", name)
		return
	}
	if iPct == serveLastProgressPct {
		return
	}
	serveLastProgressPct = iPct
	if !serveHumanIsTerminal() {
		// A file or a pipe gets whole lines at coarse steps: nothing there
		// will ever redraw a carriage-return frame.
		if iPct%25 == 0 {
			fmt.Fprintf(serveHumanOut, "  %s: %d%%\n", name, iPct)
		}
		return
	}
	fmt.Fprintf(serveHumanOut, "\r  %s: %d%%", name, iPct)
	if iPct >= 100 {
		fmt.Fprintln(serveHumanOut)
	}
}
