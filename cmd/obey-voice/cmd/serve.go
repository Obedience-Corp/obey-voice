//go:build !integration

package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	personaruntime "github.com/Obedience-Corp/obey-voice/internal/persona/runtime"
	"github.com/Obedience-Corp/obey-voice/internal/ui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

const (
	defaultServePort = 7262 // "SAMA"
	// serveCommandName is the cobra name root's PersistentPreRunE matches to
	// install serve-only process settings before it writes anything.
	serveCommandName = "serve"
)

var (
	servePersona      string
	serveBind         string
	servePort         int
	serveNoVoice      bool
	serveAllowPublic  bool
	serveTLSCert      string
	serveTLSKey       string
	serveNoMDNS       bool
	serveRevokeTokens bool
	serveRemoteMic    bool
	serveTailscale    bool
	serveBannerJSON   bool
	serveLogFile      string
	serveWorkDir      string
	serveOpen         bool
	// serveWorkDirInfo is the directory serve chdir'd into, with the layer that
	// decided it. The ready banner and /v1/status report both, so a client
	// knows where a tool call would land and whether anyone chose that.
	serveWorkDirInfo config.WorkDirInfo
	// publicHost is the hostname phones should open (MagicDNS). Empty when
	// clients should use the bind address as-is.
	servePublicHost string
	// serveTLSFallbackNote is set when --tailscale could not obtain a real
	// HTTPS cert and fell back to self-signed (still reachable over the tailnet).
	serveTLSFallbackNote string
	// serveClientSetupURL is netapi.ClientSetupURL while serve runs in limited
	// client-access mode, empty otherwise. It is the machine-readable half of
	// the "Client setup:" human line and reaches clients on the ready banner.
	serveClientSetupURL string
)

var serveCmd = &cobra.Command{
	Use:   serveCommandName,
	Short: "Serve Obey Voice to your LAN or Tailscale over HTTPS + WebSocket",
	Long: `Run Obey Voice as a network-accessible instance for any device on your
LAN or Tailscale network: phones, tablets, laptops, browsers, or
obey-voice connect. Clients send text or push-to-talk audio and stream the
live conversation over /v1/stream.

Quick paths:

  obey-voice serve --tailscale
      Remote access over Tailscale (anywhere on your tailnet): bind the 100.x
      address, print a link + pairing code, mute the host speaker. Prefers a
      trusted cert so every browser can use the mic; if that is unavailable,
      starts in limited mode (most desktop browsers work after one warning;
      the UI prints a free setup link for full mic support).

  obey-voice serve
      Same Wi‑Fi / LAN (auto-detected private bind). Same client page and
      pairing flow. Prefer --tailscale when devices are not on the same
      network, or pass --tls-cert/--tls-key for trusted HTTPS on the LAN.

  obey-voice serve --open
      After listen, open the Stage camera page in the default browser, already
      paired. The orb speaks there. Host speaker is muted so you do not get
      two voices at once; pass --no-voice=false if you want both.

Embedded voice page: obey-voice serve --open, or open the Stage: URL from the
banner. The phone client still uses Open on client: → Pair → Start.
Protocol details: docs/serve-protocol.md.

Local full voice (mic + speakers on this machine) is still "obey-voice" TUI —
serve is for remote clients, not a replacement for the conversation TUI.
SSH/Termius into the host still uses the local TUI audio path; remote mic and
speakers require the HTTPS voice page or obey-voice connect.

Auth is mandatory. --revoke-tokens invalidates the bearer. mDNS advertises
_obey-voice._tcp on the LAN (--no-mdns to disable; --tailscale implies
--no-mdns because MagicDNS is the discovery). Remote tools stay off unless
remote_tools_enabled is set.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		restoreStdio, err := configureServeStdio()
		defer restoreStdio()
		if err != nil {
			return err
		}
		if serveRevokeTokens {
			return runRevokeTokens()
		}
		if err := applyServeTailscaleDefaults(cmd); err != nil {
			return err
		}
		applyServeOpenDefaults(cmd)
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		// The agent's tools operate here. An explicit --work-dir that does not
		// exist is an error, not a silent fall back to home. Resolved after
		// configureServeStdio, which has already opened a relative --log-file
		// against the caller's cwd.
		if err := resolveConversationDirectory(cfg, servePersona, serveWorkDir, nil); err != nil {
			return err
		}
		// --persona overrides the active persona for this process only.
		// config.Load already applied the configured persona via the
		// SetAfterLoad hook, so this simply re-binds to a different one.
		if id := strings.TrimSpace(servePersona); id != "" {
			binding, err := persona.ResolveBinding(cfg, id)
			if err != nil {
				return fmt.Errorf("resolve persona: %w", err)
			}
			cfg = binding.Config()
		}
		wd, err := config.ResolveWorkDirInfo(cfg, "")
		if err != nil {
			return err
		}
		// The TLS material is read inside runServe, after the chdir.
		absPathFlag(&serveTLSCert)
		absPathFlag(&serveTLSKey)
		if err := enterWorkDir(wd.Dir); err != nil {
			return err
		}
		// Stamped onto the config so cfg and the process cwd cannot disagree.
		cfg.WorkDir = wd.Dir
		serveWorkDirInfo = wd
		return runServe(cfg)
	},
}

func init() {
	serveCmd.Flags().StringVar(&servePersona, "persona", "", "Serve as this persona for this process only (never persisted)")
	serveCmd.Flags().StringVar(&serveBind, "bind", "", "IP(s) to bind, comma-separated (default: auto-detected private LAN address + 127.0.0.1)")
	serveCmd.Flags().IntVar(&servePort, "port", defaultServePort, "Port to listen on")
	serveCmd.Flags().BoolVar(&serveNoVoice, "no-voice", false, "Do not speak responses through the local speaker")
	serveCmd.Flags().BoolVar(&serveAllowPublic, "allow-public", false, "Allow binding a non-private interface (dangerous)")
	serveCmd.Flags().StringVar(&serveTLSCert, "tls-cert", "", "TLS certificate PEM (e.g. from tailscale cert)")
	serveCmd.Flags().StringVar(&serveTLSKey, "tls-key", "", "TLS private key PEM (e.g. from tailscale cert)")
	serveCmd.Flags().BoolVar(&serveNoMDNS, "no-mdns", false, "Do not advertise via mDNS/Bonjour")
	serveCmd.Flags().BoolVar(&serveRevokeTokens, "revoke-tokens", false, "Revoke the serve bearer token, stop a running server, and exit")
	serveCmd.Flags().BoolVar(&serveRemoteMic, "remote-mic", true, "Enable remote push-to-talk (client STT over the WebSocket)")
	serveCmd.Flags().BoolVar(&serveTailscale, "tailscale", false, "Tailscale remote mode: 100.x bind, MagicDNS URL, prefer trusted cert (self-signed fallback), host speaker muted unless --no-voice=false")
	serveCmd.Flags().BoolVar(&serveBannerJSON, "banner-json", false, "Emit machine-readable JSON event lines on stdout (ready, pairing_code) and route human logs to stderr")
	serveCmd.Flags().StringVar(&serveWorkDir, "work-dir", "", "Directory the agent's file and shell tools operate in (default: the work_dir setting)")
	serveCmd.Flags().StringVar(&serveLogFile, "log-file", "", "Append human/diagnostic output to this file (created 0600) instead of the console; stdout still carries the --banner-json lines")
	serveCmd.Flags().BoolVar(&serveOpen, "open", false, "Open Stage in the default browser after listen; voice plays in the orb")
	rootCmd.AddCommand(serveCmd)
}

// applyServeTailscaleDefaults configures bind/TLS/voice when --tailscale is set.
// Explicit flags still win (bind, tls-cert, no-voice when Changed).
//
// A missing or failed `tailscale cert` no longer aborts serve: free / restricted
// tailnets often cannot mint Let's Encrypt material. We fall back to the same
// self-signed TOFU path as LAN serve so any device on the tailnet can still
// reach MagicDNS (browsers accept the warning; obey-voice connect pins the cert).
// applyServeOpenDefaults mutes the host speaker when --open is set, unless the
// user already chose --no-voice explicitly. TTS still runs; it plays in Stage
// instead of doubling with the laptop speaker.
func applyServeOpenDefaults(cmd *cobra.Command) {
	if !serveOpen {
		return
	}
	if !cmd.Flags().Changed("no-voice") {
		serveNoVoice = true
	}
	// Host mute means SinkKind may be none until Stage opts into audio_output.
	// The pipeline emits a once-per-process Info on the first no-sink synth;
	// do not arm a delayed OnListening warning — Stage often connects after
	// listen and before the first admit.
}

func applyServeTailscaleDefaults(cmd *cobra.Command) error {
	serveClientSetupURL = ""
	if !serveTailscale {
		return nil
	}
	serveTLSFallbackNote = ""

	id, err := discoverTailscale()
	if err != nil {
		return err
	}
	servePublicHost = id.DNSName

	if !cmd.Flags().Changed("bind") || serveBind == "" {
		serveBind = id.IPv4
	}
	// MagicDNS replaces LAN mDNS for remote clients on the tailnet.
	if !cmd.Flags().Changed("no-mdns") {
		serveNoMDNS = true
	}
	// Prefer remote-only audio: mute host speaker unless the user opted out.
	if !cmd.Flags().Changed("no-voice") {
		serveNoVoice = true
	}

	if serveTLSCert == "" && serveTLSKey == "" {
		tlsDir := filepath.Join(config.ConfigDir(), "serve", "tls")
		cert, key, err := ensureTailscaleCert(tlsDir, id.DNSName)
		if err != nil {
			serveTLSFallbackNote = summarizeTailscaleCertError(err)
			// G17: limited mode is the only mode with a setup link, so the
			// banner field and the human line are set from one place.
			serveClientSetupURL = netapi.ClientSetupURL
			// Stable labels for the TUI (and readable for CLI users).
			// Product outcomes only — any device, not iOS-specific.
			fmt.Fprintf(serveHumanOut, "  %s %s\n", failStyle.Render(netapi.LabelClientAccess), netapi.AccessLimited)
			fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelClientSetup), serveClientSetupURL)
			fmt.Fprintln(serveHumanOut, dimStyle.Render("  Most desktop browsers work after one warning. Full mic support on"))
			fmt.Fprintln(serveHumanOut, dimStyle.Render("  every device: open Client setup → turn on HTTPS Certificates → restart."))
			if serveTLSFallbackNote != "" {
				fmt.Fprintf(serveHumanOut, "  %s %s\n", dimStyle.Render("Detail:"), serveTLSFallbackNote)
			}
		} else {
			serveTLSCert, serveTLSKey = cert, key
			fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelClientAccess), netapi.AccessFull)
		}
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelNetwork), netapi.NetworkTailscale)
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render("Network name:"), id.DNSName)
		fmt.Fprintf(serveHumanOut, "  %s %s\n", dimStyle.Render("Bind:"), serveBind)
	}
	return nil
}

func runRevokeTokens() error {
	dir := filepath.Join(config.ConfigDir(), "serve")
	if err := netapi.RevokeTokens(dir); err != nil {
		return err
	}
	fmt.Fprintf(serveHumanOut, "  Revoked serve token under %s\n", dir)
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Any running serve instance using that token will stop."))
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Next `obey-voice serve` will mint a new token (and pairing code)."))
	return nil
}

func runServe(cfg *config.Config) error {
	ctx, cancel := signalContext()
	defer cancel()

	// Phase 3: TTS always required for stream clients. Phase 4 remote mic
	// also needs STT + VAD models when --remote-mic is set (default on).
	req := config.AssetRequest{
		NeedTTS: true,
		NeedSTT: serveRemoteMic,
		NeedVAD: serveRemoteMic,
	}
	if err := config.EnsureRuntimeAssets(ctx, cfg, req, serveModelProgress); err != nil {
		return fmt.Errorf("ensure runtime assets: %w", err)
	}

	bus := events.NewBus()
	// The event mirror is human output, so it follows serveHumanOut: under
	// --banner-json a conversation's transcript lines would otherwise land in
	// the middle of the caller's JSON stream on stdout.
	display := ui.NewWithOutput(bus, cfg.AgentName, serveHumanOut)

	// Remote turns are gated by remote_tools_enabled only. The pipeline is
	// the sole runtime tools gate (ThinkStream and ThinkFull both take
	// StreamOptions.ToolsEnabled from p.VoiceToolsEnabled), so clone config
	// and set the flag before building brains. Local voice_tools_enabled
	// must not leak tool power to the network.
	serveCfg := *cfg
	serveCfg.VoiceToolsEnabled = cfg.RemoteToolsEnabled

	p, fanout, ingress, liveTTS, cleanup, err := buildServePipeline(ctx, &serveCfg, bus, serveNoVoice, serveRemoteMic)
	if err != nil {
		return fmt.Errorf("init serve pipeline: %w", err)
	}
	defer cleanup()
	display.SetWorkSnapshot(p.WorkSnapshot)

	// Keep pipeline streaming options in lockstep with the serve policy.
	p.VoiceToolsEnabled = cfg.RemoteToolsEnabled

	ref := &sessionRef{sess: session.New(cfg.BrainProvider, serveModelName(cfg))}
	if err := ref.bindWork(p, &serveCfg, ref.sess); err != nil {
		return err
	}
	defer func() {
		if err := ref.closeWork(p); err != nil {
			fmt.Fprintf(serveHumanOut, "  warning: work cleanup: %v\n", err)
		}
	}()
	p.OnTurn = func() {
		if err := ref.save(p.Brain.History()); err != nil {
			bus.Emit(events.Error{Stage: "session", Message: fmt.Sprintf("save session: %v", err)})
		}
	}
	wireCompact(p, cfg, ref.sess, bus)
	defer func() {
		if err := ref.save(p.Brain.History()); err != nil {
			fmt.Fprintf(serveHumanOut, "  warning: failed to save session: %v\n", err)
		}
	}()

	base, err := config.LoadRaw()
	if err != nil {
		return fmt.Errorf("load base persona settings: %w", err)
	}
	base.WorkDir = serveCfg.WorkDir
	base.WorkDirSource = serveCfg.WorkDirSource
	base.VoiceToolsEnabled = serveCfg.VoiceToolsEnabled
	switcher := &servePersonaSwitcher{base: base, cfg: &serveCfg, pipeline: p, tts: liveTTS, sessions: ref}
	if _, supported := p.Brain.(brain.HistoryProjectionProvider); supported {
		if err := switcher.initialize(); err != nil {
			return err
		}
		defer switcher.selector.Close()
	}

	credsDir := filepath.Join(config.ConfigDir(), "serve")
	var creds *netapi.Credentials
	if serveTLSCert != "" || serveTLSKey != "" {
		creds, err = netapi.LoadOrCreateCredentialsWithTLS(credsDir, serveTLSCert, serveTLSKey)
	} else {
		// Stamp MagicDNS and/or the real bind IP into the self-signed cert so
		// browsers opening the printed URL pass hostname checks on LAN and
		// Tailscale (TOFU still pins the fingerprint).
		id := netapi.CertIdentity{}
		if servePublicHost != "" {
			id.DNSNames = append(id.DNSNames, servePublicHost)
		}
		for _, host := range resolveServeBindHosts() {
			if ip := net.ParseIP(host); ip != nil {
				id.IPs = append(id.IPs, ip)
			}
		}
		if len(id.DNSNames) > 0 || len(id.IPs) > 0 {
			creds, err = netapi.LoadOrCreateCredentialsWithIdentity(credsDir, id)
		} else {
			creds, err = netapi.LoadOrCreateCredentials(credsDir)
		}
	}
	if err != nil {
		return err
	}

	dispatcher := netapi.NewDispatcher(p, bus, func() {
		err := switcher.clear(ctx)
		if p.OnTurn != nil {
			p.OnTurn()
		}
		if err != nil {
			bus.Emit(events.Error{Stage: "work_clear", Message: err.Error()})
			return
		}
	}, func(id string) error { return switcher.resume(ctx, id) })
	go dispatcher.Run(ctx)
	currentSessionSnapshot := switcher.snapshotProvider()

	bindHosts := resolveServeBindHosts()
	addr := net.JoinHostPort(bindHosts[0], strconv.Itoa(servePort))
	extraBinds := make([]string, 0, len(bindHosts)-1)
	for _, host := range bindHosts[1:] {
		extraBinds = append(extraBinds, net.JoinHostPort(host, strconv.Itoa(servePort)))
	}

	sttName := ""
	if ingress != nil {
		sttName = cfg.STTProvider
	}

	// PROTOCOL_DELTAS D6: phone-driven meeting capture. It owns no audio
	// device and shares no state with the pipeline above — the phone records,
	// serve stores and transcribes afterwards.
	meetings, err := newServeMeetingManager(cfg)
	if err != nil {
		return fmt.Errorf("init meeting capture: %w", err)
	}
	defer func() { _ = meetings.Close() }()
	go meetings.RunJanitor(ctx)

	// The remote turn is bounded by the clocks the recogniser already uses:
	// listen_timeout for "no audio yet", and one utterance (phrase_time_limit)
	// past it for a client that streams but never sends voice_end.
	turnClocks := endpoint.FromConfig(cfg, false)

	var setParticipation func(context.Context, discussion.Settings) error
	if cfg.GroupParticipationEnabled && currentSessionSnapshot != nil {
		setParticipation = func(ctx context.Context, settings discussion.Settings) error {
			if err := switcher.initialize(); err != nil {
				return err
			}
			return switcher.selector.SetParticipation(ctx, settings)
		}
	}
	server := netapi.New(netapi.Options{
		SetParticipation:  setParticipation,
		SetPersonaContext: switcher.applyContext,
		ListPersonas: func() ([]netapi.PersonaSummary, error) {
			if switcher.selector != nil {
				return servePersonaList(switcher.selector.Config())()
			}
			return servePersonaList(cfg)()
		},
		Bind:                   addr,
		ExtraBinds:             extraBinds,
		AllowPublic:            serveAllowPublic,
		Credentials:            creds,
		Bus:                    bus,
		Dispatcher:             dispatcher,
		CurrentSessionSnapshot: currentSessionSnapshot,
		Audio:                  fanout,
		Ingress:                ingress,
		VoiceSilenceTimeout:    turnClocks.StartTimeout,
		VoiceTurnMax:           turnClocks.StartTimeout + turnClocks.MaxUtterance,
		Meetings:               meetings,
		RouteMeeting:           newServeMeetingRouter(cfg),
		ListSessions:           listSessionSummaries,
		DeleteSession:          serveDeleteSession(ref),
		WorkDir:                cfg.WorkDir,
		WorkDirSource:          string(serveWorkDirInfo.Source),
		LoopbackHTTP:           serveOpen,
		Campaign:               bannerCampaign(cfg.WorkDir),
		Providers: netapi.Providers{
			Brain: cfg.BrainProvider,
			STT:   sttName,
			TTS:   cfg.TTSProvider,
		},
		OnListening: func(bound []net.Addr) {
			// Advertise the real listener addresses (port 0 resolution,
			// dedupe, dual-stack formatting), never the requested strings.
			listenAddr := addr
			if len(bound) > 0 && bound[0].String() != "" {
				listenAddr = bound[0].String()
			}
			var extras []string
			if len(bound) > 1 {
				extras = make([]string, 0, len(bound)-1)
				for _, a := range bound[1:] {
					extras = append(extras, a.String())
				}
			}
			// G17 / ADR-008: binds is unconditional — a single-bind serve still
			// tells clients where it listens instead of leaving them to parse url.
			allBinds := append([]string{listenAddr}, extras...)
			if serveBannerJSON {
				emitServeBannerJSON(listenAddr, allBinds, creds, netapi.CapabilitiesForParticipation(currentSessionSnapshot != nil, setParticipation != nil))
			} else {
				printServeBanner(listenAddr, creds, cfg)
				if len(extras) > 0 {
					parts := make([]string, len(extras))
					for i, a := range extras {
						parts[i] = listenAdvertiseURL(a)
					}
					fmt.Fprintln(serveHumanOut, dimStyle.Render("  Also listening: "+strings.Join(parts, "  ")))
				}
			}
			if serveOpen {
				openStage(listenAddr, creds)
			}
			// Warm after the listener is up so a bind/config failure cannot
			// pin an Ollama runner, and a supervisor waiting on the ready
			// banner does not SIGTERM a warmup that already started.
			if w, ok := p.Brain.(brain.Warmer); ok {
				go w.Warmup(ctx)
			}
			if !serveNoMDNS {
				if disc, err := netapi.StartDiscovery(listenAddr, creds.Fingerprint, cfg.AgentName); err != nil {
					fmt.Fprintf(serveHumanOut, "  warning: mDNS advertise failed: %v\n", err)
				} else if disc != nil {
					go func() {
						<-ctx.Done()
						disc.Stop()
					}()
					fmt.Fprintln(serveHumanOut, dimStyle.Render("  mDNS: advertising "+netapi.ServiceType+" (disable with --no-mdns)"))
				}
			}
		},
	})

	return server.ListenAndServe(ctx)
}

// buildServePipeline constructs the serve-mode pipeline: text turns always,
// optional remote-mic STT on an audio.Ingress (no host capture device),
// always-on TTS for stream clients, and an AudioFanout player.
// muteHost skips the local speaker but still synthesizes for the wire.
//
// Fanout always owns the local player (if any) so cleanup is single-owner —
// no double Close between serve and buildPipeline.
// servePersonaList backs GET /v1/personas.
//
// The caller supplies the current selected binding snapshot so active reflects
// session selection without consulting the configured default.
//
// Each row overlays the profile's settings without admitting a response.
// A missing starting directory must not hide the catalog needed to repair it.
func servePersonaList(cfg *config.Config) func() ([]netapi.PersonaSummary, error) {
	return func() ([]netapi.PersonaSummary, error) {
		profiles, issues, err := persona.ListWithIssues()
		if err != nil {
			return nil, err
		}
		active := persona.ActiveID(cfg)
		out := make([]netapi.PersonaSummary, 0, len(profiles))
		for _, p := range profiles {
			snap := cfg.Clone()
			persona.Apply(snap, p)
			id, displayName := p.ID, p.DisplayName
			brainModel := persona.ModelForProvider(snap, snap.BrainProvider)
			brainProvider := snap.BrainProvider
			out = append(out, netapi.PersonaSummary{
				StartingDirectory: p.StartingDirectory,
				ID:                id,
				DisplayName:       displayName,
				Active:            id == active,
				Builtin:           p.Builtin,
				Inactive:          p.Inactive,
				Brain: netapi.PersonaBrain{
					Provider: brainProvider,
					Model:    brainModel,
				},
				TTS: netapi.PersonaTTS{
					Provider: snap.TTSProvider,
					Voice:    activeVoiceFor(snap),
					Tier:     activeTierFor(snap),
				},
			})
		}
		if len(issues) > 0 {
			warnings := &netapi.PersonaListIssues{}
			for _, issue := range issues {
				warnings.Issues = append(warnings.Issues, netapi.PersonaListIssue{
					ID: issue.ID, Message: "Repair persona.yaml: " + issue.Err.Error(),
				})
			}
			return out, warnings
		}
		return out, nil
	}
}

// servePersonaSwitcher binds the shared selection operation to serve's
// serialized dispatcher. Selection persists in the normal session file.
type servePersonaSwitcher struct {
	base     *config.Config
	selector *personaruntime.Selector
	cfg      *config.Config
	pipeline *pipeline.Pipeline
	tts      *voiceagent.LiveTTSManager
	sessions *sessionRef

	newBrain func(*config.Config) (brain.Provider, error)
	newTTS   func(*config.Config) (*voiceagent.TTSSet, error)
}

// serveDeleteSession builds the DELETE /v1/sessions/{id} callback: refuses
// with netapi.ErrSessionActive when id is the session ref currently holds
// (this process rewrites that file on every turn, so deleting it would just
// make it reappear), otherwise deletes it from the default session store.
// session.ErrSessionNotFound is translated to netapi's own sentinel so
// handleSessionDelete can tell "no such session" (404) apart from an
// unexpected store failure (500) without importing the session package.
func serveDeleteSession(ref *sessionRef) func(id string) error {
	return func(id string) error {
		if id == ref.id() {
			return netapi.ErrSessionActive
		}
		err := session.DefaultStore().Delete(id)
		if errors.Is(err, session.ErrSessionNotFound) {
			return netapi.ErrSessionNotFound
		}
		return err
	}
}

// initialize binds the shared operation to the existing session and work owner.
func (s *servePersonaSwitcher) initialize() error {
	if s.selector != nil {
		return nil
	}
	base := s.base
	if base == nil {
		snap := *s.cfg
		base = &snap
	}
	binding, err := persona.ResolveBinding(s.cfg, persona.ActiveID(s.cfg))
	if err != nil {
		return err
	}
	opts := personaruntime.SelectionOptions{Base: base, Pipeline: s.pipeline, Voices: s.tts,
		SessionID: s.sessions.id(), Initial: binding,
		Participation: s.sessions.sess.Participation,
		PersistParticipation: func(settings discussion.Settings) error {
			s.sessions.mu.Lock()
			defer s.sessions.mu.Unlock()
			return session.DefaultStore().SaveParticipation(s.sessions.sess, settings)
		},
		Persist: func(turns []brain.Turn, selected events.PersonaSelection) error {
			s.sessions.mu.Lock()
			defer s.sessions.mu.Unlock()
			return session.DefaultStore().SaveSelectionState(s.sessions.sess, turns, selected)
		},
	}
	if s.newBrain != nil {
		opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) { return s.newBrain(cfg) }
	}
	if s.newTTS != nil {
		opts.NewVoice = func(_ context.Context, cfg *config.Config) (*voiceagent.TTSSet, error) { return s.newTTS(cfg) }
	}
	s.selector, err = personaruntime.NewSelector(opts)
	if err != nil {
		return err
	}
	s.pipeline.AdmitInput = s.selector.Route
	s.pipeline.RouteHandoff = s.selector.RouteHandoff
	s.pipeline.EndDiscussion = s.selector.EndDiscussion
	s.pipeline.CompleteDiscussion = s.selector.RecordDiscussionResponse
	return nil
}

func (s *servePersonaSwitcher) apply(id string) (netapi.PersonaAck, error) {
	return s.applyContext(context.Background(), id)
}

func (s *servePersonaSwitcher) applyContext(ctx context.Context, id string) (netapi.PersonaAck, error) {
	if err := s.initialize(); err != nil {
		return netapi.PersonaAck{}, err
	}
	selected, err := s.selector.Select(ctx, id, "ui")
	if err != nil {
		return netapi.PersonaAck{}, err
	}
	return netapi.PersonaAck{ID: selected.PersonaID, DisplayName: selected.DisplayName, PromptHash: selected.PromptHash}, nil
}

// clear starts a new conversation epoch while retaining the selected
// responder. Selector publication must follow the work epoch transition so a
// subsequent session snapshot cannot combine the old selection with new work.
func (s *servePersonaSwitcher) clear(ctx context.Context) error {
	if err := s.pipeline.ClearConversation(); err != nil {
		return err
	}
	if s.selector == nil {
		return nil
	}
	selected := s.selector.Snapshot()
	s.selector.RestoreSession(s.sessions.id(), selected.PersonaID, selected.DisplayName, selected.PromptHash, selected.Directories)
	_, err := s.selector.Select(ctx, selected.PersonaID, "clear")
	return err
}

// snapshotProvider advertises replay only when all snapshot authorities exist.
// Conversation identity is host-owned in ordinary sessions and coordinator-
// owned when optional work is enabled; neither mode needs a worker to replay
// canonical history.
func (s *servePersonaSwitcher) snapshotProvider() netapi.SnapshotProvider {
	if s.selector == nil || !s.pipeline.ConversationScope().Valid() || s.pipeline.ConversationRetired() {
		return nil
	}
	if _, ok := s.pipeline.Brain.(brain.CanonicalHistoryProvider); !ok {
		return nil
	}
	return s.snapshot
}

// snapshot is called by netapi only at a dispatcher boundary. Canonical
// history is the source for reconnect identity; provider projection History is
// deliberately not used here.
func (s *servePersonaSwitcher) snapshot(ctx context.Context) (netapi.SessionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return netapi.SessionSnapshot{}, err
	}
	if s.selector == nil {
		return netapi.SessionSnapshot{}, errors.New("shared-persona session snapshot is unavailable")
	}
	canonical, ok := s.pipeline.Brain.(brain.CanonicalHistoryProvider)
	if !ok {
		return netapi.SessionSnapshot{}, errors.New("canonical conversation history is unavailable")
	}
	selected := s.selector.Snapshot()
	s.sessions.mu.Lock()
	if s.sessions.sess == nil {
		s.sessions.mu.Unlock()
		return netapi.SessionSnapshot{}, errors.New("active session is unavailable")
	}
	sessionID := s.sessions.sess.ID
	s.sessions.mu.Unlock()
	scope := s.pipeline.ConversationScope()
	if sessionID == "" || scope.SessionID != sessionID || selected.SessionID != sessionID {
		return netapi.SessionSnapshot{}, errors.New("session snapshot has mismatched session identity")
	}
	if scope.ConversationEpoch == "" || selected.ConversationEpoch != scope.ConversationEpoch {
		return netapi.SessionSnapshot{}, errors.New("session snapshot has mismatched conversation epoch")
	}
	var settings *discussion.Settings
	if s.cfg.GroupParticipationEnabled {
		copy := s.selector.Participation()
		settings = &copy
	}
	return netapi.SessionSnapshot{
		Participation: settings, Discussion: s.selector.DiscussionSnapshot(),
		SessionID:         sessionID,
		ConversationEpoch: scope.ConversationEpoch,
		Selection:         selected,
		Transcript:        canonical.CanonicalHistory(),
	}, nil
}

// resume is a conversation transition, separate from responder selection.
// A missing saved profile does not hide the restored transcript: responses
// remain disabled until the client explicitly selects a valid persona.
func (s *servePersonaSwitcher) resume(ctx context.Context, id string) error {
	cfg := s.cfg
	if s.selector != nil {
		cfg = s.selector.Config()
	}
	next, err := session.Load(id)
	if err != nil {
		return err
	}
	cfg = cfg.Clone()
	if err := resolveConversationDirectory(cfg, next.SelectedPersonaID, serveWorkDir, next); err != nil {
		return err
	}
	// Providers without shared selection cannot replace a live brain safely.
	// Validate before touching the old session; reopening serve can restore it.
	if s.selector == nil && cfg.WorkDir != s.cfg.WorkDir {
		return fmt.Errorf("resuming a different directory requires a provider with shared conversation selection")
	}
	if err := s.sessions.resume(s.pipeline, cfg, id); err != nil {
		return err
	}
	s.sessions.mu.Lock()
	restored := *s.sessions.sess
	s.sessions.mu.Unlock()
	wireCompact(s.pipeline, cfg, s.sessions.sess, s.pipeline.Events)
	if s.selector == nil {
		return nil
	}
	if err := s.selector.RestoreParticipation(restored.Participation); err != nil {
		return err
	}
	s.selector.RestoreSession(restored.ID, restored.SelectedPersonaID, restored.SelectedPersonaDisplayName, restored.SelectedPersonaPromptHash, cfg.ConversationDirectories)
	_, err = s.selector.Select(ctx, restored.SelectedPersonaID, "resume")
	if err != nil && s.pipeline.Events != nil {
		s.pipeline.Events.Emit(events.Error{Stage: "persona", Message: fmt.Sprintf("Conversation restored; select a valid persona before continuing: %v", err)})
	}
	return nil
}

// buildServePipeline builds serve's pipeline through voiceagent.Options.
//
// This is the surface test for the library. Serve is the awkward consumer: it
// wants the brain but not the host microphone, TTS but a fanout instead of a
// speaker, and on remote-mic it feeds STT from a network ingress rather than a
// capture device. If Options cannot express that, the surface is wrong — so the
// swaps are passed IN rather than assigned onto the pipeline afterwards.
//
// Serve builds its override providers first, so their cleanup stays here. The
// live manager owns the initial TTS set and later persona replacements; a
// replaced worker is closed after a drain so in-flight audio can finish.
func buildServePipeline(ctx context.Context, cfg *config.Config, bus *events.Bus, muteHost, remoteMic bool) (*pipeline.Pipeline, *netapi.AudioFanout, *audio.Ingress, *voiceagent.LiveTTSManager, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(err error) (*pipeline.Pipeline, *netapi.AudioFanout, *audio.Ingress, *voiceagent.LiveTTSManager, func(), error) {
		cleanup()
		return nil, nil, nil, nil, nil, err
	}

	ttsSet, err := voiceagent.NewTTSSet(cfg)
	if err != nil {
		return fail(fmt.Errorf("init TTS: %w", err))
	}
	liveTTS := &voiceagent.LiveTTSManager{}
	if !liveTTS.Adopt(ttsSet) {
		ttsSet.Close()
		return fail(fmt.Errorf("init TTS: conversation is shutting down"))
	}
	cleanups = append(cleanups, liveTTS.Close)
	if ttsSet.FallbackWarning != nil {
		fmt.Fprintf(serveDiagOut, "warning: %v\n", ttsSet.FallbackWarning)
	}

	var local audio.Engine
	if !muteHost {
		player := playback.NewPlayerWithDevice(cfg.OutputDevice)
		player.SetVolume(config.PlaybackVolumeOf(cfg))
		local = player
	}
	// Fanout owns local so Close is exactly once via cleanup. SinkKind is
	// none until a Stage client opts into audio_output; the pipeline warns
	// once per process on the first synthesize with no listener.
	fanout := netapi.NewOwnedAudioFanout(local)
	cleanups = append(cleanups, func() { _ = fanout.Close() })

	// Captured, not read per call: the pipeline logs from turn goroutines that
	// serve never joins, and the package var is restored when serve returns.
	diag := serveDiagOut
	opts := voiceagent.Options{
		Config:      cfg,
		Events:      bus,
		TextOnly:    true, // no host mic
		Silent:      false,
		TTS:         ttsSet.Primary,
		TTSFallback: ttsSet.Fallback,
		Player:      fanout,
		Logf:        func(format string, args ...any) { fmt.Fprintf(diag, format+"\n", args...) },
	}

	var ingress *audio.Ingress
	if remoteMic {
		// Remote push-to-talk: STT reads from network PCM, not the host mic.
		// Force VAD so sherpa/whisper paths can build (config may leave it off).
		serveSTTCfg := *cfg
		if !serveSTTCfg.VADEnabled {
			serveSTTCfg.VADEnabled = true
		}
		ingress = audio.NewIngress()
		cleanups = append(cleanups, func() { _ = ingress.Close() })

		vad, err := audio.NewVAD(&serveSTTCfg)
		if err != nil {
			return fail(fmt.Errorf("init VAD for remote mic: %w", err))
		}
		cleanups = append(cleanups, vad.Delete)

		sttProvider, sttCleanup, err := stt.NewProvider(&serveSTTCfg, ingress, vad)
		if err != nil {
			return fail(fmt.Errorf("init STT for remote mic: %w", err))
		}
		if sttCleanup != nil {
			cleanups = append(cleanups, sttCleanup)
		}
		opts.STT = sttProvider
		opts.VAD = vad
		// Capture stays nil: barge-in on host mic is not used in serve mode.
	}

	agent, baseCleanup, err := voiceagent.New(ctx, opts)
	if err != nil {
		return fail(err)
	}
	// Seeded at the FRONT so it runs LAST: the library's resources were acquired
	// before serve's overrides in dependency terms, and reverse-order teardown
	// must still hold across the two stacks.
	cleanups = append([]func(){baseCleanup}, cleanups...)

	return agent.Pipeline, fanout, ingress, liveTTS, cleanup, nil
}

func serveModelName(cfg *config.Config) string {
	switch cfg.BrainProvider {
	case "claude":
		return "claude"
	case "grok":
		if cfg.GrokModel != "" {
			return cfg.GrokModel
		}
		return "grok"
	default:
		return cfg.OllamaModel
	}
}

func listSessionSummaries() []netapi.SessionSummary {
	sessions := session.List()
	out := make([]netapi.SessionSummary, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, netapi.SessionSummary{
			ID:        s.ID,
			Summary:   s.Summary,
			Turns:     len(s.Turns),
			UpdatedAt: s.UpdatedAt,
		})
	}
	return out
}

// defaultServeBind picks the machine's private LAN or tailnet (CGNAT) address,
// falling back to loopback when none is found — never a public interface by
// default.
func defaultServeBind() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	var tailscale string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil {
			continue
		}
		if ip.IsPrivate() {
			return ip.String()
		}
		// Prefer RFC1918 when present; remember a Tailscale/CGNAT address as
		// a secondary choice for machines that only have a tailnet IP.
		if tailscale == "" && netapi.IsTrustedServeIP(ip) && !ip.IsLoopback() {
			tailscale = ip.String()
		}
	}
	if tailscale != "" {
		return tailscale
	}
	return "127.0.0.1"
}

// resolveServeBindHosts returns the hosts this serve binds. The first host is
// primary: it feeds the banner URL, mDNS, and QR pairing payloads. Loopback is
// appended in tailscale mode (G35 / ADR-008) so the host app on this machine
// keeps a route to its own agent — never promoted, so the tailnet address
// stays the one remote clients are told to open.
func resolveServeBindHosts() []string {
	hosts := explicitOrAutoBindHosts()
	if serveTailscale && !bindsReachLoopback(hosts) {
		hosts = append(hosts, "127.0.0.1")
	}
	return hosts
}

// explicitOrAutoBindHosts returns the comma-separated --bind list verbatim when
// set (tailscale mode sets it to the tailnet IP), else the auto-detected
// private LAN address plus loopback so local clients (the Mac app,
// same-machine tools) and LAN devices reach one serve.
func explicitOrAutoBindHosts() []string {
	if serveBind != "" {
		hosts := make([]string, 0, 2)
		for h := range strings.SplitSeq(serveBind, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) > 0 {
			return hosts
		}
	}
	hosts := []string{defaultServeBind()}
	if !bindsReachLoopback(hosts) {
		hosts = append(hosts, "127.0.0.1")
	}
	return hosts
}

// bindsReachLoopback reports whether this host list already answers on the
// machine's own loopback — named explicitly ("127.0.0.1", "::1", "localhost"),
// or covered by an unspecified bind ("0.0.0.0", "::") that takes every
// interface. Adding a second loopback listener in either case gains no
// reachability, and against an unspecified bind it would fail the port is
// already taken.
func bindsReachLoopback(hosts []string) bool {
	for _, h := range hosts {
		h = strings.Trim(strings.TrimSpace(h), "[]")
		if strings.EqualFold(h, "localhost") {
			return true
		}
		ip := net.ParseIP(h)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// sessionRef holds the session remote turns save into; resume swaps it while
// the dispatcher guarantees no turn is mid-flight.
type sessionRef struct {
	mu         sync.Mutex
	sess       *session.Session
	checkpoint *workCheckpoint
}

func (r *sessionRef) save(turns []brain.Turn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoint.retry()
	return r.sess.Save(turns)
}

func (r *sessionRef) bindWork(p *pipeline.Pipeline, cfg *config.Config, sess *session.Session) error {
	checkpoint, err := bindWorkSession(p, cfg, sess, session.DefaultStore())
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.sess, r.checkpoint = sess, checkpoint
	r.mu.Unlock()
	return nil
}

func (r *sessionRef) closeWork(p *pipeline.Pipeline) error {
	r.mu.Lock()
	checkpoint := r.checkpoint
	r.mu.Unlock()
	return closeWorkSession(p, checkpoint)
}

// resume runs at a dispatcher boundary, after the current turn is joined.
// Checkpoint callbacks retain their own session; none follows this ref into
// the newly selected conversation.
func (r *sessionRef) resume(p *pipeline.Pipeline, cfg *config.Config, id string) error {
	next, err := session.Load(id)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	previousID := r.id()
	history := p.Brain.History()
	if err := r.save(history); err != nil {
		return fmt.Errorf("save previous session: %w", err)
	}
	if err := r.closeWork(p); err != nil {
		return fmt.Errorf("close previous conversation work: %w", err)
	}
	// Resuming the current session must include its final work checkpoint.
	if id == previousID {
		next, err = session.Load(id)
		if err != nil {
			return err
		}
	}
	p.Brain.LoadHistory(next.Turns)
	if err := r.bindWork(p, cfg, next); err != nil {
		p.Brain.LoadHistory(history)
		previous, loadErr := session.Load(previousID)
		if loadErr == nil {
			loadErr = r.bindWork(p, cfg, previous)
		}
		return errors.Join(fmt.Errorf("resume conversation work: %w", err), loadErr)
	}
	return nil
}

func (r *sessionRef) swap(sess *session.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sess = sess
}

// id returns the id of the session currently in use, for the DELETE
// /v1/sessions/{id} live-session guard: a serve process rewrites this file on
// every turn, so deleting it out from under the pipeline would just make it
// reappear.
func (r *sessionRef) id() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sess.ID
}
