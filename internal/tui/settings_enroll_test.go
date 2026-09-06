package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// enrollCapture is a mic that publishes a constant tone to every subscriber as
// fast as it is read, so a "3 second" clip costs no wall clock. silent leaves
// the channel empty, which is how a stopped device looks from here.
type enrollCapture struct {
	silent bool

	mu     sync.Mutex
	next   int
	subs   map[int]chan struct{} // id -> stop
	closed int
}

func newEnrollCapture() *enrollCapture {
	return &enrollCapture{subs: map[int]chan struct{}{}}
}

func (c *enrollCapture) Subscribe(buffer int) (int, <-chan []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := c.next
	stop := make(chan struct{})
	c.subs[id] = stop
	ch := make(chan []float32, max(buffer, 1))
	if c.silent {
		return id, ch
	}
	go func() {
		chunk := make([]float32, audio.ChunkSize)
		for i := range chunk {
			chunk[i] = 0.25
		}
		for {
			select {
			case <-stop:
				return
			case ch <- chunk:
			}
		}
	}()
	return id, ch
}

func (c *enrollCapture) Unsubscribe(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if stop, ok := c.subs[id]; ok {
		close(stop)
		delete(c.subs, id)
		c.closed++
	}
}

func (c *enrollCapture) released() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// enrollTestSettings wires a Speakers model whose store lives in a temp config
// root, with a fake mic and a fake embedding model. Nothing here touches the
// host's config dir, a real device, or sherpa.
func enrollTestSettings(t *testing.T, capture speaker.CaptureSource) (*settingsModel, *tuiLiveSpeakerFake) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())

	cfg := &config.Config{UserDisplayName: "Lance"}
	m := newSettings(cfg, nil)
	m.section = sectionSpeakers
	m.enrollGap = 0
	m.saveConfig = func(string, any) error { return nil }
	if capture != nil {
		m.captureFor = func() (speaker.CaptureSource, func(), error) { return capture, func() {}, nil }
	}
	m.speakerEngine = func(context.Context) (speaker.Engine, func(), error) {
		return &speaker.FakeEngine{EmbedDim: 4, EmbedRev: "fake-rev-w1500"}, func() {}, nil
	}
	live := &tuiLiveSpeakerFake{}
	m.liveSeeder = func() LiveSpeakerController { return live }
	return &m, live
}

// TestRegisterRecordsThreeClipsAndSeedsTheRunningEngine is the slice: three
// clips off the conversation's capture graph, one enrolled owner, and a live
// engine that knows the name without a restart.
func TestRegisterRecordsThreeClipsAndSeedsTheRunningEngine(t *testing.T) {
	capture := newEnrollCapture()
	m, live := enrollTestSettings(t, capture)

	msg, ok := m.recordEnrollment(t.Context(), "Lance", newEventBridge(64))().(enrollDoneMsg)
	if !ok {
		t.Fatalf("recordEnrollment returned %T, want enrollDoneMsg", msg)
	}
	if msg.err != nil {
		t.Fatalf("registration failed: %v", msg.err)
	}
	if msg.profile.Name != "Lance" || !msg.profile.Owner {
		t.Fatalf("profile = %+v, want the owner named Lance", msg.profile)
	}
	// Three clips of enrollClipLength at the default 1500 ms window embed as
	// two rows each: the store must hold what was recorded, not one whole-span
	// embed (the FRR 0.80 shape windowed enrollment exists to avoid).
	if msg.profile.Samples != enrollClips*2 {
		t.Errorf("profile samples = %d, want %d", msg.profile.Samples, enrollClips*2)
	}
	// One subscription per clip, each released with its clip: the fan-out is
	// never held across the pause between clips.
	if capture.released() != enrollClips {
		t.Errorf("capture subscriptions released = %d, want %d", capture.released(), enrollClips)
	}

	if !msg.seeded || msg.seedErr != nil {
		t.Fatalf("seeded=%v seedErr=%v, want a seeded live engine", msg.seeded, msg.seedErr)
	}
	if len(live.seeded) != 1 {
		t.Fatalf("SeedEnrolled called %d times, want once", len(live.seeded))
	}
	got := live.seeded[0]
	if got.name != "Lance" || got.modelRev != msg.profile.ModelRev || len(got.embeddings) != msg.profile.Samples {
		t.Fatalf("seeded %+v, want Lance/%s with %d rows", got, msg.profile.ModelRev, msg.profile.Samples)
	}

	// The store on disk is the record; the row labels read it back.
	store, err := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg))
	if err != nil || store == nil {
		t.Fatalf("store after registration: %v (%v)", store, err)
	}
	if owner, ok := store.Owner(); !ok || owner.Name != "Lance" {
		t.Fatalf("stored owner = %+v (%v), want Lance", owner, ok)
	}
}

// TestRecordEnrollClipsWritesThenRemovesTheClips: the WAVs are temporary, and
// each is long enough for EnrollFromWAVs' minimum.
func TestRecordEnrollClipsWritesThenRemovesTheClips(t *testing.T) {
	capture := newEnrollCapture()
	captureFor := func() (speaker.CaptureSource, func(), error) { return capture, func() {}, nil }

	paths, cleanup, err := recordEnrollClips(t.Context(), captureFor, newEventBridge(64), 0)
	if err != nil {
		t.Fatalf("recordEnrollClips: %v", err)
	}
	if len(paths) != enrollClips {
		t.Fatalf("recorded %d clips, want %d", len(paths), enrollClips)
	}
	for _, path := range paths {
		samples, rate, err := audio.ReadWAVFloat32(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if rate != audio.SampleRate {
			t.Errorf("%s sample rate = %d, want %d", path, rate, audio.SampleRate)
		}
		if got := len(samples); got < enrollClipSamples() {
			t.Errorf("%s has %d samples, want at least %d", path, got, enrollClipSamples())
		}
	}
	cleanup()
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("clip %s survived cleanup: %v", path, err)
		}
	}
}

// TestRegisterCancelledMidRecordEnrollsNothing: Esc during recording must not
// leave a half-voice in the store.
func TestRegisterCancelledMidRecordEnrollsNothing(t *testing.T) {
	m, live := enrollTestSettings(t, &enrollCapture{silent: true, subs: map[int]chan struct{}{}})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	msg, ok := m.recordEnrollment(ctx, "Lance", newEventBridge(64))().(enrollDoneMsg)
	if !ok || !errors.Is(msg.err, context.Canceled) {
		t.Fatalf("cancelled registration = %+v, want context.Canceled", msg)
	}
	if len(live.seeded) != 0 {
		t.Errorf("a cancelled registration seeded the live engine: %+v", live.seeded)
	}
	if store, err := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg)); err != nil || store != nil {
		t.Fatalf("store = %v (%v), want none created by a cancelled registration", store, err)
	}

	// And the overlay closes with the cancellation, not an error.
	m.enroll = enrollSession{active: true, name: "Lance"}
	m.finishEnroll(msg)
	if m.enroll.active {
		t.Error("the overlay must close when the registration ends")
	}
	if !strings.Contains(m.message, "Cancelled") {
		t.Errorf("message = %q, want a cancellation", m.message)
	}
}

// TestRegisterWithoutMicrophoneEnrollsNothing: Settings opened where no
// capture graph exists says so instead of failing obscurely.
func TestRegisterWithoutMicrophoneEnrollsNothing(t *testing.T) {
	m, _ := enrollTestSettings(t, nil) // no captureFor

	if cmd := m.startEnroll("Lance", true); cmd != nil {
		t.Fatal("startEnroll must not launch a recorder without a microphone")
	}
	if m.enroll.active {
		t.Error("no session may be armed without a microphone")
	}
	if !strings.Contains(m.message, "Microphone unavailable") {
		t.Errorf("message = %q, want Microphone unavailable", m.message)
	}
	if store, _ := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg)); store != nil {
		t.Error("a mic-less registration must not create a store")
	}
}

// TestRegisterWithoutNameAsksForItFirst: the profile is stored under a name,
// so an empty Settings name opens that editor and resumes on commit.
func TestRegisterWithoutNameAsksForItFirst(t *testing.T) {
	capture := newEnrollCapture()
	m, _ := enrollTestSettings(t, capture)
	m.cfg.UserDisplayName = ""
	m.cursor = speakerRowRegister

	cmd := m.selectCurrent()
	if cmd == nil || m.edit.kind != editUserDisplayName {
		t.Fatalf("register with no name must open the name editor (kind=%v)", m.edit.kind)
	}
	if m.enroll.active {
		t.Fatal("recording must not start before the name is committed")
	}

	// An empty commit is not a name: nothing records.
	if empty := commitTyped(*m, "  "); empty.enroll.active {
		t.Fatal("an empty name must not start a recording")
	}

	m.enrollAfterName = true
	next := commitTyped(*m, "Lance")
	if !next.enroll.active || next.enroll.name != "Lance" {
		t.Fatalf("enroll session = %+v, want an active session for Lance", next.enroll)
	}
	next.cancelEnroll()
}

// TestRemoveVoiceNeedsAConfirmation: only "y" deletes a voiceprint.
func TestRemoveVoiceNeedsAConfirmation(t *testing.T) {
	capture := newEnrollCapture()
	m, _ := enrollTestSettings(t, capture)
	if msg, _ := m.recordEnrollment(t.Context(), "Lance", newEventBridge(64))().(enrollDoneMsg); msg.err != nil {
		t.Fatalf("setup registration failed: %v", msg.err)
	}
	m.refreshEnrolledProfiles()
	if !m.enrolledOwnerOK {
		t.Fatal("setup did not leave an enrolled owner")
	}

	m.cursor = speakerRowRemove
	m.selectCurrent()
	if m.confirmRemoveVoice != "Lance" {
		t.Fatalf("confirmRemoveVoice = %q, want Lance", m.confirmRemoveVoice)
	}

	kept, _ := m.updateRemoveVoiceConfirm(key("n"))
	if !strings.Contains(kept.message, "Kept Lance") {
		t.Errorf("message = %q, want Kept Lance", kept.message)
	}
	if store, _ := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg)); store == nil || len(store.List()) != 1 {
		t.Fatal("any key but y must keep the profile")
	}

	m.confirmRemoveVoice = "Lance"
	removed, _ := m.updateRemoveVoiceConfirm(key("y"))
	if !strings.Contains(removed.message, "speaker-1") {
		t.Errorf("message = %q, want the speaker-1 fallback explained", removed.message)
	}
	if !strings.Contains(removed.message, "open conversation") {
		t.Errorf("message = %q, want the running-engine caveat when a controller exists", removed.message)
	}
	store, _ := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg))
	if store != nil && len(store.List()) != 0 {
		t.Fatalf("profiles after removal = %+v, want none", store.List())
	}
	if removed.enrolledOwnerOK {
		t.Error("the row cache still claims an owner after removal")
	}
}

// TestSpeakerRegisterRowsFollowTheStore covers the three label states a user
// reads to know what pressing enter will do.
func TestSpeakerRegisterRowsFollowTheStore(t *testing.T) {
	m, _ := enrollTestSettings(t, newEnrollCapture())

	if got := m.registerRowLabel(); got != "Register your voice" {
		t.Errorf("empty-store register row = %q", got)
	}
	if got := m.removeRowLabel(); !strings.Contains(got, "nothing registered") {
		t.Errorf("empty-store remove row = %q", got)
	}

	m.enrolledOwner = speaker.Profile{Name: "Lance", ModelRev: speaker.ExpectedLiveRev(speaker.FromAppConfig(m.cfg))}
	m.enrolledOwnerOK = true
	if got := m.registerRowLabel(); got != "Re-enroll your voice — Lance" {
		t.Errorf("enrolled register row = %q", got)
	}
	if got := m.removeRowLabel(); got != "Remove your voice — Lance" {
		t.Errorf("enrolled remove row = %q", got)
	}

	// A window change restamps the recipe, so the stored voice no longer
	// matches what live analysis produces — same copy `speaker list` uses.
	m.enrolledOwner.ModelRev = "titanet-w9999"
	if got := m.registerRowLabel(); !strings.Contains(got, "re-enroll to refresh") {
		t.Errorf("stale register row = %q, want the refresh hint", got)
	}

	// The Remove row is inert until something is registered.
	m.enrolledOwnerOK = false
	m.cursor = speakerRowRemove
	m.selectCurrent()
	if m.confirmRemoveVoice != "" {
		t.Error("removing with nothing registered must not arm a confirmation")
	}
}

// TestEnrollOverlayOwnsTheScreen: while recording, the list is replaced and the
// keys belong to the overlay — a stray "q" must not leave with the mic open.
func TestEnrollOverlayOwnsTheScreen(t *testing.T) {
	m, _ := enrollTestSettings(t, newEnrollCapture())
	m.width, m.height = 100, 24
	m.enroll = enrollSession{active: true, name: "Lance", clip: 2, elapsed: 1500 * time.Millisecond, claimOwner: true}

	view := stripANSI(m.View())
	for _, want := range []string{"Register your voice", "Recording clip 2 of 3", "microphone is live", "Esc cancels"} {
		if !strings.Contains(view, want) {
			t.Errorf("overlay missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Match threshold") {
		t.Errorf("the list must be replaced while recording:\n%s", view)
	}

	next, _ := m.Update(key("q"))
	if !next.enroll.active {
		t.Error("a stray key must not close the recording overlay")
	}
}

// key builds a rune KeyMsg for the confirmation and overlay paths.
func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestReEnrollSeedsTheRunningEngineAgain: "Re-enroll your voice" is the second
// registration under the same name, and the seat it writes to is already
// occupied — in the store and in the running engine's embedding manager. It has
// to land in both, and the status line must not tell the user the open
// conversation kept its old labels.
func TestReEnrollSeedsTheRunningEngineAgain(t *testing.T) {
	capture := newEnrollCapture()
	m, live := enrollTestSettings(t, capture)

	first, _ := m.recordEnrollment(t.Context(), "Lance", newEventBridge(64))().(enrollDoneMsg)
	if first.err != nil {
		t.Fatalf("first registration failed: %v", first.err)
	}

	again, ok := m.recordEnrollment(t.Context(), "Lance", newEventBridge(64))().(enrollDoneMsg)
	if !ok || again.err != nil {
		t.Fatalf("re-enroll = %+v, want a successful registration", again)
	}
	if !again.seeded || again.seedErr != nil {
		t.Fatalf("re-enroll seeded=%v seedErr=%v, want the running engine to take it", again.seeded, again.seedErr)
	}
	if len(live.seeded) != 2 || live.seeded[1].name != "Lance" {
		t.Fatalf("seed calls = %+v, want a second seed for Lance", live.seeded)
	}
	// Re-enrolling refreshes embeddings, not identity: still one owner.
	if !again.profile.Owner {
		t.Error("re-enrolling the owner must keep ownership")
	}
	store, err := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg))
	if err != nil || store == nil {
		t.Fatalf("store after re-enroll: %v (%v)", store, err)
	}
	if got := len(store.List()); got != 1 {
		t.Fatalf("profiles after re-enroll = %d, want 1", got)
	}

	m.enroll = enrollSession{active: true, name: "Lance"}
	m.finishEnroll(again)
	if !strings.Contains(m.message, "Registered Lance") {
		t.Errorf("message = %q, want a successful registration", m.message)
	}
	for _, unwanted := range []string{"rejected", "keeps its old labels", "next conversation"} {
		if strings.Contains(m.message, unwanted) {
			t.Errorf("message = %q, must not claim %q after a seeded re-enroll", m.message, unwanted)
		}
	}
}

func seedEnrolledProfile(t *testing.T, m *settingsModel, name, rev string) speaker.Profile {
	t.Helper()
	store, err := speaker.OpenEnrollment(speaker.EnrollmentDir(m.cfg))
	if err != nil {
		t.Fatalf("OpenEnrollment: %v", err)
	}
	p, err := store.Add(t.Context(), name, rev, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	if err != nil {
		t.Fatalf("Add %s: %v", name, err)
	}
	m.refreshEnrolledProfiles()
	return p
}

func speakerKindIndex(m settingsModel, kind int) int {
	for i, row := range m.speakerRows() {
		if row.kind == kind {
			return i
		}
	}
	return -1
}

func speakerGuestIndex(m settingsModel, name string) int {
	for i, row := range m.speakerRows() {
		if row.kind == speakerRowGuest && row.name == name {
			return i
		}
	}
	return -1
}

func TestEnrollAnotherOpensNameEditorWithoutTouchingDisplayName(t *testing.T) {
	m, _ := enrollTestSettings(t, newEnrollCapture())
	seedEnrolledProfile(t, m, "Lance", "fake-rev-w1500")

	idx := speakerKindIndex(*m, speakerRowEnrollOther)
	if idx < 0 {
		t.Fatal("enroll another row missing after an owner is stored")
	}
	m.cursor = idx
	if cmd := m.selectCurrent(); cmd == nil {
		t.Fatal("enroll another must open the name editor")
	}
	if m.edit.kind != editSpeakerEnrollName {
		t.Fatalf("edit kind = %v, want editSpeakerEnrollName", m.edit.kind)
	}
	next := commitTyped(*m, "Alex")
	if next.cfg.UserDisplayName != "Lance" {
		t.Fatalf("display name = %q, enroll another must not rewrite it", next.cfg.UserDisplayName)
	}
	if !next.enroll.active || next.enroll.name != "Alex" || next.enroll.claimOwner {
		t.Fatalf("enroll session = %+v, want active guest session for Alex", next.enroll)
	}
	next.cancelEnroll()
}

func TestEnrollAnotherRejectsInvalidAndOwnerNames(t *testing.T) {
	m, _ := enrollTestSettings(t, newEnrollCapture())
	seedEnrolledProfile(t, m, "Lance", "fake-rev-w1500")
	m.cursor = speakerKindIndex(*m, speakerRowEnrollOther)
	m.selectCurrent()

	kept := commitTyped(*m, "speaker-1")
	if kept.edit.kind != editSpeakerEnrollName {
		t.Fatalf("invalid name closed the editor (kind=%v)", kept.edit.kind)
	}
	if kept.enroll.active {
		t.Fatal("reserved name must not start recording")
	}
	if !strings.Contains(kept.message, "reserved") {
		t.Errorf("message = %q, want reserved", kept.message)
	}

	owner := commitTyped(kept, "Lance")
	if owner.edit.kind != editSpeakerEnrollName {
		t.Fatalf("owner name closed the editor (kind=%v)", owner.edit.kind)
	}
	if owner.enroll.active {
		t.Fatal("owner name must not start a guest recording")
	}
	if !strings.Contains(owner.message, "Re-enroll your voice") {
		t.Errorf("message = %q, want the re-enroll hint", owner.message)
	}
}

func TestGuestEnrollDoesNotClaimOwnership(t *testing.T) {
	capture := newEnrollCapture()
	m, live := enrollTestSettings(t, capture)
	if msg, _ := m.recordEnrollment(t.Context(), "Lance", newEventBridge(64))().(enrollDoneMsg); msg.err != nil {
		t.Fatalf("owner setup: %v", msg.err)
	}
	m.refreshEnrolledProfiles()

	m.enroll.claimOwner = false
	msg, ok := m.recordEnrollment(t.Context(), "Alex", newEventBridge(64))().(enrollDoneMsg)
	if !ok || msg.err != nil {
		t.Fatalf("guest enroll = %+v", msg)
	}
	if msg.profile.Owner {
		t.Fatal("guest enroll must not become owner")
	}
	if !msg.seeded || live.seeded[len(live.seeded)-1].name != "Alex" {
		t.Fatalf("guest was not seeded: %+v", live.seeded)
	}
	store, err := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg))
	if err != nil || store == nil {
		t.Fatalf("store: %v %v", store, err)
	}
	if got := len(store.List()); got != 2 {
		t.Fatalf("profiles = %d, want 2", got)
	}
	if owner, ok := store.Owner(); !ok || owner.Name != "Lance" {
		t.Fatalf("owner = %+v ok=%v, want Lance", owner, ok)
	}
}

func TestGuestEnrollAfterOwnerRemovedDoesNotClaimSeat(t *testing.T) {
	m, _ := enrollTestSettings(t, newEnrollCapture())
	if msg, _ := m.recordEnrollment(t.Context(), "Lance", newEventBridge(64))().(enrollDoneMsg); msg.err != nil {
		t.Fatalf("owner setup: %v", msg.err)
	}
	m.enroll.claimOwner = false
	if msg, _ := m.recordEnrollment(t.Context(), "Alex", newEventBridge(64))().(enrollDoneMsg); msg.err != nil {
		t.Fatalf("guest setup: %v", msg.err)
	}
	m.refreshEnrolledProfiles()
	m.removeVoice("Lance")
	if m.enrolledOwnerOK {
		t.Fatal("setup did not clear the owner")
	}

	m.enroll.claimOwner = false
	msg, ok := m.recordEnrollment(t.Context(), "Dana", newEventBridge(64))().(enrollDoneMsg)
	if !ok || msg.err != nil {
		t.Fatalf("Dana enroll = %+v", msg)
	}
	if msg.profile.Owner {
		t.Fatal("enroll another after owner-remove must not SetOwner")
	}
	store, err := speaker.LoadEnrollment(speaker.EnrollmentDir(m.cfg))
	if err != nil || store == nil {
		t.Fatalf("store: %v %v", store, err)
	}
	if _, owned := store.Owner(); owned {
		t.Fatal("store must still have an empty owner seat")
	}
}

func TestGuestRowReenrollAndRemove(t *testing.T) {
	m, _ := enrollTestSettings(t, newEnrollCapture())
	if msg, _ := m.recordEnrollment(t.Context(), "Lance", newEventBridge(64))().(enrollDoneMsg); msg.err != nil {
		t.Fatalf("owner setup: %v", msg.err)
	}
	m.enroll.claimOwner = false
	if msg, _ := m.recordEnrollment(t.Context(), "Alex", newEventBridge(64))().(enrollDoneMsg); msg.err != nil {
		t.Fatalf("guest setup: %v", msg.err)
	}
	m.refreshEnrolledProfiles()

	idx := speakerGuestIndex(*m, "Alex")
	if idx < 0 {
		t.Fatal("Alex guest row missing")
	}
	m.cursor = idx
	cmd := m.selectCurrent()
	if cmd == nil || !m.enroll.active || m.enroll.name != "Alex" || m.enroll.claimOwner {
		t.Fatalf("re-enroll session = %+v, want Alex guest", m.enroll)
	}
	if title := m.enrollOverlayTitle(); title != "Re-enroll Alex" {
		t.Errorf("overlay title = %q, want Re-enroll Alex", title)
	}
	m.cancelEnroll()
	m.enroll = enrollSession{}

	m.cursor = idx
	next, _ := m.Update(key("d"))
	if next.confirmRemoveVoice != "Alex" {
		t.Fatalf("confirmRemoveVoice = %q, want Alex", next.confirmRemoveVoice)
	}
	kept, _ := next.updateRemoveVoiceConfirm(key("n"))
	if !strings.Contains(kept.message, "Kept Alex") {
		t.Errorf("message = %q, want Kept Alex", kept.message)
	}

	m.confirmRemoveVoice = "Alex"
	removed, _ := m.updateRemoveVoiceConfirm(key("y"))
	if !strings.Contains(removed.message, "Removed Alex") {
		t.Errorf("message = %q, want Removed Alex", removed.message)
	}
	if strings.Contains(removed.message, "speaker-1") {
		t.Errorf("guest remove must not claim the speaker-1 fallback: %q", removed.message)
	}
	if speakerGuestIndex(removed, "Alex") >= 0 {
		t.Fatal("Alex guest row still present after remove")
	}
	if !removed.enrolledOwnerOK || removed.enrolledOwner.Name != "Lance" {
		t.Fatalf("owner after guest remove = %+v ok=%v", removed.enrolledOwner, removed.enrolledOwnerOK)
	}
}
