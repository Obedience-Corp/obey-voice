package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const (
	// enrollClips is how many clips one registration records. Three short,
	// distinct utterances is what `speaker enroll` already recommends: enough
	// windows for the manager to average over, short enough to sit through.
	enrollClips = 3
	// enrollClipGap is the pause between clips, so the user can draw breath
	// and start a new sentence instead of being cut off mid-word.
	enrollClipGap = time.Second
)

// enrollClipLength is how long each clip runs: long enough to split into two
// live-sized analysis windows at the default 1500 ms. A var rather than a
// const so tests can shrink a clip and still afford a real inter-clip gap.
var enrollClipLength = 3 * time.Second

// errNoMicrophone is the one enrollment failure the user can act on without a
// stack trace: there is no capture graph to record from. The status line shows
// enrollNoMicMessage for it instead of the wrapped error text.
var errNoMicrophone = errors.New("microphone unavailable")

const enrollNoMicMessage = "Microphone unavailable — open a conversation first, or check Settings → Input"

// enrollSession is an in-flight "Register your voice" or guest enrollment.
// Zero value = idle.
type enrollSession struct {
	active bool
	name   string
	cancel context.CancelFunc
	events *eventBridge
	// clip is 1-based; elapsed is how much of it has been captured.
	clip      int
	elapsed   time.Duration
	enrolling bool // clips are recorded; the model is embedding them
	// claimOwner is set for Register your voice so an empty owner seat is
	// claimed even when CLI guests already occupy the store.
	claimOwner bool
}

type enrollProgressMsg struct {
	clip    int
	elapsed time.Duration
}

// enrollEnrollingMsg marks the switch from recording to embedding, which is
// where a first-time run pays for the model load.
type enrollEnrollingMsg struct{}

// enrollClosedMsg releases the last armed bridge read once the worker is done,
// so no Cmd goroutine is left blocked on a channel nothing will write to.
type enrollClosedMsg struct{}

type enrollDoneMsg struct {
	profile speaker.Profile
	seeded  bool  // a running live engine took the profile
	seedErr error // enrolled, but the running engine refused the seed
	err     error
}

// initEnrollSeams installs the production implementations of the registration
// seams. captureFor is left nil: only the App knows whether a conversation is
// running, and Settings must never open a mic the screen cannot see.
func (m *settingsModel) initEnrollSeams(cfg *config.Config) {
	m.enrollGap = enrollClipGap
	m.enrollmentStore = func() (*speaker.Enrollment, error) {
		return speaker.OpenEnrollment(speaker.EnrollmentDir(cfg))
	}
	m.speakerEngine = func(ctx context.Context) (speaker.Engine, func(), error) {
		return newEnrollEngine(ctx, cfg)
	}
	m.refreshEnrolledProfiles()
}

// newEnrollEngine ensures the managed embedding model and loads it, the same
// two steps `speaker enroll` takes — an install that never enabled speakers
// downloads TitaNet here, on the first registration.
func newEnrollEngine(ctx context.Context, cfg *config.Config) (speaker.Engine, func(), error) {
	assetCfg := *cfg
	assetCfg.Speaker.Enabled = true
	assetCfg.Speaker.Live.Enabled = true
	if err := config.EnsureRuntimeAssets(ctx, &assetCfg, config.AssetRequest{NeedSpeaker: true}, nil); err != nil {
		return nil, nil, fmt.Errorf("prepare the speaker model: %w", err)
	}
	sp := speaker.FromAppConfig(&assetCfg)
	engine, err := speaker.NewEmbeddingEngine(sp, config.ModelsDirFrom(&assetCfg))
	if err != nil {
		return nil, nil, err
	}
	return engine, func() { _ = engine.Close() }, nil
}

// refreshEnrolledProfiles re-reads the store for the Speakers row labels.
// Read-only on purpose: LoadEnrollment never creates the directory, so opening
// Settings on an install that never enrolled leaves the config dir untouched.
func (m *settingsModel) refreshEnrolledProfiles() {
	m.enrolledProfiles = nil
	m.enrolledOwner, m.enrolledOwnerOK = speaker.Profile{}, false
	store, err := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg))
	if err != nil || store == nil {
		return
	}
	m.enrolledProfiles = store.List()
	m.enrolledOwner, m.enrolledOwnerOK = store.Owner()
}

// ownerRevIsStale reports whether the enrolled owner was embedded by a
// different recipe than the one live analysis would use now (a changed
// speaker.live.window_ms is the usual cause), which SeedFromStore skips.
func (m settingsModel) ownerRevIsStale() bool {
	if !m.enrolledOwnerOK {
		return false
	}
	return m.enrolledOwner.ModelRev != speaker.ExpectedLiveRev(speaker.FromAppConfig(m.cfg))
}

// beginEnroll starts a registration. The profile is stored under a name, and
// an empty one is not a name — so a blank Settings name opens that editor
// first and the recording starts on a non-empty commit.
func (m *settingsModel) beginEnroll() tea.Cmd {
	if m.enroll.active {
		return nil
	}
	name := strings.TrimSpace(m.cfg.UserDisplayName)
	if name == "" {
		m.enrollAfterName = true
		return m.beginEdit(editUserDisplayName, "Your name", "")
	}
	return m.startEnroll(name, true)
}

// startEnroll arms the session and launches the recorder off the update loop.
// claimOwner is true for Register your voice and false for guest enroll.
func (m *settingsModel) startEnroll(name string, claimOwner bool) tea.Cmd {
	if m.captureFor == nil {
		m.message = enrollNoMicMessage
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := newEventBridge(16)
	m.enroll = enrollSession{active: true, name: name, cancel: cancel, events: events, clip: 1, claimOwner: claimOwner}
	m.message = ""
	return tea.Batch(events.wait(), m.recordEnrollment(ctx, name, events))
}

// cancelEnroll stops an in-flight registration — Esc, or leaving Settings.
// The worker observes the canceled context and reports it, so the overlay is
// torn down there and a half-recorded voice is never enrolled.
func (m *settingsModel) cancelEnroll() {
	if m.enroll.cancel != nil {
		m.enroll.cancel()
	}
}

// updateEnrollKey owns every key while the overlay is up: a stray "q" must not
// walk out of Settings with the microphone still subscribed.
func (m settingsModel) updateEnrollKey(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	if msg.String() == "esc" {
		m.cancelEnroll()
	}
	return m, nil
}

// updateEnroll folds the worker's messages into the overlay and re-arms the
// progress bridge, the same drain pattern the Qwen install uses.
func (m settingsModel) updateEnroll(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case enrollProgressMsg:
		if !m.enroll.active {
			return m, nil
		}
		m.enroll.clip, m.enroll.elapsed = msg.clip, msg.elapsed
	case enrollEnrollingMsg:
		if !m.enroll.active {
			return m, nil
		}
		m.enroll.enrolling = true
	case enrollClosedMsg:
		// The worker is finished: stop draining rather than re-arming.
		return m, nil
	case enrollDoneMsg:
		m.finishEnroll(msg)
		return m, nil
	}
	if m.enroll.events != nil {
		return m, m.enroll.events.wait()
	}
	return m, nil
}

// finishEnroll closes the overlay and reports what the store now holds.
func (m *settingsModel) finishEnroll(msg enrollDoneMsg) {
	if !m.enroll.active {
		// The screen was rebuilt while the worker ran (Settings left and
		// re-entered): nothing to close, and no message to overwrite.
		return
	}
	claimOwner := m.enroll.claimOwner
	m.enroll = enrollSession{}
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.message = "Cancelled — nothing registered"
	case errors.Is(msg.err, errNoMicrophone):
		m.message = enrollNoMicMessage
	case msg.err != nil:
		m.message = fmt.Sprintf("Registration failed: %v", msg.err)
	default:
		m.refreshEnrolledProfiles()
		verb := "Enrolled"
		if msg.profile.Owner || claimOwner {
			verb = "Registered"
		}
		m.message = fmt.Sprintf("%s %s — live labels will use it", verb, msg.profile.Name)
		if msg.seedErr != nil {
			m.message += fmt.Sprintf(" (the open conversation keeps its old labels: %v)", msg.seedErr)
		} else if !msg.seeded {
			m.message += " · starts on the next conversation"
		}
	}
}

// beginRemoveVoice arms the owner removal confirmation. Nothing is deleted here.
func (m *settingsModel) beginRemoveVoice() {
	if !m.enrolledOwnerOK {
		m.message = "Nothing registered yet — choose Register your voice"
		return
	}
	m.beginRemoveVoiceNamed(m.enrolledOwner.Name)
}

func (m *settingsModel) beginRemoveVoiceNamed(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	m.confirmRemoveVoice = name
	m.message = ""
}

// updateRemoveVoiceConfirm answers a pending removal. Only "y" deletes, so a
// stray enter on this screen cannot destroy a voiceprint.
func (m settingsModel) updateRemoveVoiceConfirm(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	name := m.confirmRemoveVoice
	m.confirmRemoveVoice = ""
	if msg.String() != "y" && msg.String() != "Y" {
		m.message = fmt.Sprintf("Kept %s", name)
		return m, nil
	}
	m.removeVoice(name)
	return m, nil
}

// removeVoice deletes the stored voiceprint. A live engine that already seeded
// it keeps the name until it is rebuilt — there is no unseed call, so the
// message says so rather than pretending the label changes immediately.
func (m *settingsModel) removeVoice(name string) {
	openStore := m.enrollmentStore
	if openStore == nil {
		m.message = "Speaker enrollment is unavailable"
		return
	}
	store, err := openStore()
	if err != nil {
		m.message = fmt.Sprintf("Failed to open the speaker store: %v", err)
		return
	}
	if err := store.Remove(name); err != nil {
		m.message = fmt.Sprintf("Failed to remove %s: %v", name, err)
		return
	}
	wasOwner := m.isOwnerName(name)
	m.refreshEnrolledProfiles()
	if maxIdx := m.currentListLen() - 1; m.cursor > maxIdx {
		m.cursor = max(maxIdx, 0)
	}
	if wasOwner {
		m.message = fmt.Sprintf("Removed %s. Live labels will say speaker-1 until you register again.", name)
	} else {
		m.message = fmt.Sprintf("Removed %s.", name)
	}
	if m.liveSeeder != nil && m.liveSeeder() != nil {
		m.message += " The open conversation keeps the old voice until it restarts."
	}
}

// settingsBoxWidth sizes an overlay to the terminal, matching the Personas
// forms so the two screens do not disagree about how wide a dialog is.
func (m settingsModel) settingsBoxWidth() int {
	return max(min(m.renderWidth()-2, 78), 24)
}

// enrollOverlayLines replaces the list while a registration runs. The mic note
// is load-bearing: entering Settings force-mutes the conversation, and frames
// keep flowing regardless — the user has to be told the recording is real.
func (m settingsModel) enrollOverlayLines() []string {
	var body []string
	if m.enroll.enrolling {
		body = []string{
			normalStyle.Render(fmt.Sprintf("Enrolling %s…", m.enroll.name)),
			"",
			dimStyle.Render(fmt.Sprintf("Embedding %d clips with the speaker model.", enrollClips)),
			dimStyle.Render("The first registration also downloads it."),
		}
	} else {
		clip := max(m.enroll.clip, 1)
		body = []string{
			normalStyle.Render(fmt.Sprintf("Recording clip %d of %d — say a full sentence in your normal voice…", clip, enrollClips)),
			"",
			statusStyle.Render(enrollProgressBar(m.enroll.elapsed, 24)),
			"",
			warningStyle.Render("The microphone is live even though the conversation is muted."),
			dimStyle.Render("Esc cancels — nothing is saved until all three clips are recorded."),
		}
	}
	box := formBoxStyled(headerStyle.Render(m.enrollOverlayTitle()), body, m.settingsBoxWidth(), lipgloss.NewStyle().Foreground(colorAccent))
	return append([]string{""}, box...)
}

func (m settingsModel) enrollOverlayTitle() string {
	if m.enroll.claimOwner || m.isOwnerName(m.enroll.name) {
		return "Register your voice"
	}
	for _, p := range m.enrolledProfiles {
		if !p.Owner && strings.EqualFold(p.Name, m.enroll.name) {
			return fmt.Sprintf("Re-enroll %s", p.Name)
		}
	}
	if strings.TrimSpace(m.enroll.name) == "" {
		return "Enroll a speaker"
	}
	return fmt.Sprintf("Enroll %s", m.enroll.name)
}

// enrollProgressBar renders elapsed against one clip's length.
func enrollProgressBar(elapsed time.Duration, width int) string {
	total := enrollClipLength
	filled := 0
	if total > 0 {
		filled = min(int(float64(width)*elapsed.Seconds()/total.Seconds()), width)
	}
	return fmt.Sprintf("[%s%s]  %.1fs / %.1fs",
		strings.Repeat("█", max(filled, 0)), strings.Repeat("·", width-max(filled, 0)),
		elapsed.Seconds(), total.Seconds())
}

// confirmRemoveVoiceLines renders the removal prompt in place of the list.
func (m settingsModel) confirmRemoveVoiceLines() []string {
	name := m.confirmRemoveVoice
	body := []string{
		normalStyle.Render(fmt.Sprintf("Remove %s's voice?", name)),
		"",
		dimStyle.Render("Deletes the stored voiceprint and its embeddings."),
		dimStyle.Render("Transcripts already written keep the name."),
	}
	if m.isOwnerName(name) {
		body = append(body, dimStyle.Render("Chat labels fall back to speaker-1 until you register again."))
	}
	body = append(body,
		"",
		normalStyle.Render("Press ")+errorStyle.Render("y")+normalStyle.Render(" to remove · any other key keeps it"),
	)
	box := formBoxStyled(errorStyle.Render("Confirm remove"), body, m.settingsBoxWidth(), errorStyle)
	return append([]string{""}, box...)
}
