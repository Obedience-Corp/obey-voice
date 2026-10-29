package qwen

import (
	"context"
	"encoding/json"
	"fmt"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"net"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// speechWorkerLabel prefixes the LAN transport's protocol errors.
const speechWorkerLabel = "speech worker"

// newRemoteQwen3TTS builds the qwen3-tts provider against a speech worker on
// another computer. No local worker binary, no GGUF, no models dir: this host
// contributes text and plays PCM. The provider name is unchanged — place is
// topology, not a provider (D13).
func newRemoteQwen3TTS(cfg *config.Config) (*Qwen3TTS, error) {
	target, err := config.SpeechDialTargetOf(cfg)
	if err != nil {
		return nil, fmt.Errorf("qwen3-tts: %w", err)
	}

	timeout := defaultQwenTTSTimeout
	timeoutSet := false
	if cfg.QwenTTSTimeout > 0 {
		timeout = time.Duration(cfg.QwenTTSTimeout) * time.Second
		timeoutSet = true
	}

	q := newQwen3TTS("", target.Address, timeout, nil)
	q.timeoutSet = timeoutSet
	q.native = true
	q.remote = true
	q.remoteAddr = target.Address
	q.modelTier = managedqwen.NormalizeModelTier(cfg.QwenTTSModelTier)
	q.modelsDir = config.ModelsDirFrom(cfg)
	q.consent = cfg.QwenTTSConsent
	q.mode = tts.VoiceMode(strings.TrimSpace(cfg.QwenTTSMode))
	if q.mode == "" || q.mode == tts.VoiceModeStatic {
		q.mode = tts.VoiceModeCustomVoice
	}
	q.voice = remoteVoice(cfg.QwenTTSVoice)
	q.language = managedqwen.DefaultLanguage
	if language, ok := managedqwen.CanonicalLanguage(strings.TrimSpace(cfg.QwenTTSLanguage)); ok {
		q.language = language
	}
	q.streamMode = config.QwenTTSStreamOf(cfg)
	// No backend hint over the LAN: the install manifest describes this
	// computer, which is not the one synthesizing. Eligibility for lan does not
	// consult it (see config.QwenStreamEligible).
	q.alive.Store(true)
	q.startSpeech = func(ctx context.Context) (qwenSpeechSession, error) {
		return dialRemoteQwenSession(ctx, cfg)
	}

	session, err := q.startSpeech(context.Background())
	if err != nil {
		q.alive.Store(false)
		return nil, err
	}
	q.applyStreamPolicy(session)
	q.primarySlot().adopt(session)
	return q, nil
}

// remoteVoice resolves the configured voice for a LAN session. A shipped clone
// is a ref_wav throat, and obey-speech/v1 does not carry one, so it degrades to
// the speech worker's own default rather than failing every turn.
func remoteVoice(configured string) string {
	voice := strings.TrimSpace(configured)
	if voice == "" || strings.EqualFold(voice, "default") {
		return ""
	}
	if _, shipped := managedqwen.LookupShipped(voice); shipped {
		return ""
	}
	if name, _, ok := managedqwen.ResolveVoice(voice); ok {
		return name
	}
	return voice
}

// remoteQwenSession speaks obey-speech/v1 to a speech-serve host. After the
// hello the framing is identical to the child worker's, so everything below
// the handshake is the shared speechSession.
type remoteQwenSession struct {
	*speechSession

	netConn net.Conn
	address string
	tier    string
}

// dialRemoteQwenSession dials, says hello and reads ready. The child worker
// never sees the hello: speech-serve holds its stdio ready until this
// handshake succeeds (spec/30 §2).
func dialRemoteQwenSession(ctx context.Context, cfg *config.Config) (*remoteQwenSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	netConn, target, err := config.DialSpeechWorker(ctx, cfg)
	if err != nil {
		return nil, speechUnavailable("dial speech worker", err)
	}

	// The timeout bounds dial and handshake only; a cold speech-serve spends
	// seconds loading its model behind an already-accepted socket, and the
	// synthesis that follows is not bounded by it either.
	handshake := time.Duration(config.TTSLanTimeoutMSOf(cfg)) * time.Millisecond
	if err := netConn.SetDeadline(time.Now().Add(handshake)); err != nil {
		netConn.Close()
		return nil, speechUnavailable("dial speech worker", err)
	}

	session := &remoteQwenSession{
		speechSession: &speechSession{
			conn:           newSpeechConn(speechWorkerLabel, config.SpeechProtocol, netConn, netConn, netConn),
			label:          speechWorkerLabel,
			referenceAudio: false, // ref_wav is not carried over the LAN in v1
			rate:           qwen3TTSSampleRate,
		},
		netConn: netConn,
		address: target.Address,
	}

	// A cancelled dial must not wait out the handshake deadline: expiring the
	// socket's own deadline is what unblocks the read.
	handshakeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = netConn.SetDeadline(time.Now())
		case <-handshakeDone:
		}
	}()

	ready, err := session.handshake(ctx, config.TTSWorkerTokenOf(cfg))
	close(handshakeDone)
	if err != nil {
		netConn.Close()
		return nil, err
	}
	if err := netConn.SetDeadline(time.Time{}); err != nil {
		netConn.Close()
		return nil, speechUnavailable("dial speech worker", err)
	}
	session.streaming = ready.Streaming
	session.capable = ready.streamCapable()
	session.presets = append([]string(nil), ready.Presets...)
	session.tier = strings.TrimSpace(ready.Tier)
	return session, nil
}

// handshake writes the hello frame and validates the one ready line that
// answers it. A refusal names no reason: the server does not tell a caller
// whether the token was wrong or missing.
func (s *remoteQwenSession) handshake(ctx context.Context, token string) (speechReadyMsg, error) {
	hello, err := config.SpeechHelloLine(token)
	if err != nil {
		return speechReadyMsg{}, speechUnavailable("speech worker hello", err)
	}
	if _, err := s.netConn.Write(hello); err != nil {
		return speechReadyMsg{}, speechUnavailable("speech worker hello", err)
	}
	if err := ctx.Err(); err != nil {
		return speechReadyMsg{}, err
	}
	line, err := s.conn.ReadLineBlocking()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return speechReadyMsg{}, ctxErr
		}
		return speechReadyMsg{}, speechUnavailable("speech worker ready", err)
	}
	var refusal struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &refusal); err == nil && refusal.Type == "error" {
		message := strings.TrimSpace(refusal.Message)
		if message == "" {
			message = "rejected"
		}
		return speechReadyMsg{}, speechUnavailable("speech worker hello", fmt.Errorf("handshake refused: %s", message))
	}
	ready, err := s.conn.ParseReady(line)
	if err != nil {
		return speechReadyMsg{}, speechUnavailable("speech worker ready", err)
	}
	if err := s.conn.ValidateReady(ready); err != nil {
		return speechReadyMsg{}, speechUnavailable("speech worker ready", err)
	}
	return ready, nil
}

// Tier is what the speech worker reports about its own model, when it reports
// anything. The child never sends it, so it is display-only.
func (s *remoteQwenSession) Tier() string {
	if s == nil {
		return ""
	}
	return s.tier
}

// Close sends the shutdown line and drops the socket. speech-serve intercepts
// a client shutdown and keeps its child warm for the next conversation
// (spec/30 §3), so this never costs a model load.
func (s *remoteQwenSession) Close() {
	if !s.beginClose() {
		return
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.netConn != nil {
		_ = s.netConn.Close()
	}
}

func speechUnavailable(operation string, err error) error {
	return &tts.ProviderError{
		Provider:  qwen3TTSProviderName,
		Operation: operation,
		Kind:      tts.ProviderErrorUnavailable,
		Err:       err,
	}
}
