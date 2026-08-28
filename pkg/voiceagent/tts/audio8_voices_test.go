package tts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const testFingerprint = "audio8-tts-preview-0.1b-int8-v1"

func testCodes(codebooks, frames int) [][]int64 {
	rows := make([][]int64, codebooks)
	for r := range rows {
		rows[r] = make([]int64, frames)
		for c := range rows[r] {
			rows[r][c] = int64(r*frames + c)
		}
	}
	return rows
}

func testMeta(name string) Audio8VoiceMeta {
	return Audio8VoiceMeta{
		Name:             name,
		Transcript:       "this is the reference line",
		ModelFingerprint: testFingerprint,
		SampleRate:       44100,
		Provenance:       Audio8VoiceProvenance{Kind: Audio8VoiceRecorded, SourceFile: "ref.wav", Consent: true},
	}
}

func TestSaveAudio8VoiceRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name  string
		meta  Audio8VoiceMeta
		codes [][]int64
		want  string
	}{
		{"empty name", Audio8VoiceMeta{}, testCodes(10, 4), "voice name is required"},
		{
			"a path escapes the store",
			Audio8VoiceMeta{Name: "../escape", Transcript: "x", ModelFingerprint: testFingerprint},
			testCodes(10, 4), "invalid voice name",
		},
		{
			"the packaged voice is reserved",
			Audio8VoiceMeta{Name: config.Audio8PackagedVoice, Transcript: "x", ModelFingerprint: testFingerprint},
			testCodes(10, 4), "reserved",
		},
		{
			"no transcript",
			Audio8VoiceMeta{Name: "alice", ModelFingerprint: testFingerprint},
			testCodes(10, 4), "needs the transcript",
		},
		{
			"no fingerprint",
			Audio8VoiceMeta{Name: "alice", Transcript: "x"},
			testCodes(10, 4), "needs the model fingerprint",
		},
		{"no codes", testMeta("alice"), nil, "has no codes"},
		{
			"a transcript of only whitespace",
			Audio8VoiceMeta{Name: "alice", Transcript: " \n \t ", ModelFingerprint: testFingerprint},
			testCodes(10, 4), "needs the transcript",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SaveAudio8Voice(dir, tt.meta, tt.codes)
			if err == nil {
				t.Fatalf("SaveAudio8Voice() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("SaveAudio8Voice() error = %q, want it to mention %q", err, tt.want)
			}
			if names := config.Audio8RegisteredVoiceNames(dir); len(names) != 0 {
				t.Errorf("a refused registration left %v behind", names)
			}
		})
	}
}

func TestAudio8VoiceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	codes := testCodes(10, 37)
	ragged := testMeta("alice")
	// A transcript pasted out of a script arrives with line breaks in it; the
	// store keeps one canonical form because it is re-tokenized every turn.
	ragged.Transcript = "  this is\n  the   reference line "
	if err := SaveAudio8Voice(dir, ragged, codes); err != nil {
		t.Fatalf("SaveAudio8Voice: %v", err)
	}

	meta, err := LoadAudio8VoiceMeta(dir, "alice")
	if err != nil {
		t.Fatalf("LoadAudio8VoiceMeta: %v", err)
	}
	if meta.Transcript != "this is the reference line" {
		t.Errorf("transcript = %q", meta.Transcript)
	}
	if meta.Frames != 37 || meta.NumCodebooks != 10 {
		t.Errorf("meta = %d codebooks x %d frames, want 10 x 37", meta.NumCodebooks, meta.Frames)
	}
	if meta.CreatedAt.IsZero() {
		t.Error("created_at was not stamped")
	}
	if got := meta.Seconds(); got < 1.6 || got > 1.8 {
		t.Errorf("Seconds() = %.3f, want ~1.72 (37 frames at %.2f Hz)", got, audio8CodecFrameRateHz)
	}

	// The codes must come back through the same loader the packaged reference
	// uses, byte for byte.
	got, _, err := loadAudio8VoiceCodes(dir, "alice", testFingerprint, 10)
	if err != nil {
		t.Fatalf("loadAudio8VoiceCodes: %v", err)
	}
	for r := range codes {
		for c := range codes[r] {
			if got[r][c] != codes[r][c] {
				t.Fatalf("codes[%d][%d] = %d, want %d", r, c, got[r][c], codes[r][c])
			}
		}
	}
}

func TestLoadAudio8VoiceCodesRefusesAForeignModel(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAudio8Voice(dir, testMeta("alice"), testCodes(10, 8)); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadAudio8VoiceCodes(dir, "alice", "audio8-some-other-revision", 10)
	if err == nil {
		t.Fatal("loadAudio8VoiceCodes() error = nil, want a fingerprint mismatch")
	}
	if !strings.Contains(err.Error(), "audio8-some-other-revision") || !strings.Contains(err.Error(), testFingerprint) {
		t.Errorf("error = %q, want it to name both fingerprints", err)
	}
}

func TestLoadAudio8VoiceMetaReportsAMissingVoice(t *testing.T) {
	_, err := LoadAudio8VoiceMeta(t.TempDir(), "nobody")
	if !errors.Is(err, ErrAudio8VoiceNotFound) {
		t.Fatalf("LoadAudio8VoiceMeta() error = %v, want ErrAudio8VoiceNotFound", err)
	}
}

// TestSaveAudio8VoiceReplacesAtomically checks that re-registering a name does
// not leave the two files describing different recordings.
func TestSaveAudio8VoiceReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAudio8Voice(dir, testMeta("alice"), testCodes(10, 8)); err != nil {
		t.Fatal(err)
	}
	second := testMeta("alice")
	second.Transcript = "a different line entirely"
	second.CreatedAt = time.Now().UTC()
	if err := SaveAudio8Voice(dir, second, testCodes(10, 21)); err != nil {
		t.Fatal(err)
	}

	meta, err := LoadAudio8VoiceMeta(dir, "alice")
	if err != nil {
		t.Fatal(err)
	}
	codes, _, err := loadAudio8VoiceCodes(dir, "alice", testFingerprint, 10)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Frames != 21 || len(codes[0]) != 21 {
		t.Fatalf("after replacement meta says %d frames and codes hold %d, want 21 and 21", meta.Frames, len(codes[0]))
	}
	if meta.Transcript != second.Transcript {
		t.Errorf("transcript = %q, want the replacement's", meta.Transcript)
	}
	// No staging directory survives.
	entries, err := os.ReadDir(config.Audio8VoicesDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("store holds %d entries, want just the voice", len(entries))
	}
}

func TestRemoveAudio8Voice(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAudio8Voice(dir, testMeta("alice"), testCodes(10, 8)); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAudio8Voice(dir, config.Audio8PackagedVoice); err == nil {
		t.Error("RemoveAudio8Voice(reference) error = nil, want the packaged voice to be protected")
	}
	if err := RemoveAudio8Voice(dir, "nobody"); !errors.Is(err, ErrAudio8VoiceNotFound) {
		t.Errorf("RemoveAudio8Voice(nobody) error = %v, want ErrAudio8VoiceNotFound", err)
	}
	if err := RemoveAudio8Voice(dir, "alice"); err != nil {
		t.Fatalf("RemoveAudio8Voice: %v", err)
	}
	if names := config.Audio8RegisteredVoiceNames(dir); len(names) != 0 {
		t.Errorf("after removal the store lists %v", names)
	}
}

func TestListAudio8VoicesReportsACorruptEntry(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAudio8Voice(dir, testMeta("alice"), testCodes(10, 8)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.Audio8VoiceDir(dir, "alice"), config.Audio8VoiceMetaFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListAudio8Voices(dir); err == nil {
		t.Fatal("ListAudio8Voices() error = nil; a voice the user cannot see must say why")
	}
}

func TestAudio8VoiceCatalog(t *testing.T) {
	dir := t.TempDir()
	recorded := testMeta("alice")
	recorded.Locale = "en-GB"
	if err := SaveAudio8Voice(dir, recorded, testCodes(10, 8)); err != nil {
		t.Fatal(err)
	}
	synth := testMeta("bootstrapped")
	synth.Locale = "en-us"
	synth.Provenance = Audio8VoiceProvenance{
		Kind: Audio8VoiceSynthetic, BootstrapProvider: "kokoro", BootstrapVoice: "af_heart",
	}
	if err := SaveAudio8Voice(dir, synth, testCodes(10, 8)); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		locale, gender string
		want           []string
	}{
		{"unfiltered lists the packaged voice first", "", "", []string{config.Audio8PackagedVoice, "alice", "bootstrapped"}},
		{"locale filters case-insensitively", "en-gb", "", []string{"alice"}},
		{"a locale nothing matches is empty", "de", "", nil},
		{"provenance is the gender column", "", Audio8VoiceSynthetic, []string{"bootstrapped"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			voices, err := audio8VoiceCatalog(dir, tt.locale, tt.gender)
			if err != nil {
				t.Fatalf("audio8VoiceCatalog: %v", err)
			}
			var got []string
			for _, v := range voices {
				got = append(got, v.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("catalog = %v, want %v", got, tt.want)
			}
		})
	}

	voices, err := audio8VoiceCatalog(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range voices {
		if v.Name == "bootstrapped" && !strings.Contains(v.FriendlyName, "af_heart") {
			t.Errorf("synthetic voice reads %q; the bootstrap source must be visible", v.FriendlyName)
		}
	}
}

// TestNPYRoundTripsThroughTheReader is the format contract: what the store
// writes is what the provider's existing .npy reader accepts.
func TestNPYRoundTripsThroughTheReader(t *testing.T) {
	tests := []struct{ codebooks, frames int }{{10, 1}, {10, 110}, {10, 646}, {3, 7}}
	for _, tt := range tests {
		path := filepath.Join(t.TempDir(), "codes.npy")
		want := testCodes(tt.codebooks, tt.frames)
		if err := writeNPYInt64Rows(path, want); err != nil {
			t.Fatalf("writeNPYInt64Rows(%dx%d): %v", tt.codebooks, tt.frames, err)
		}
		// numpy requires the data to start on a 64-byte boundary.
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if header := 10 + int(raw[8]) + int(raw[9])<<8; header%64 != 0 {
			t.Errorf("data starts at byte %d, want a multiple of 64", header)
		}
		got, err := loadNPYInt64Rows(path, tt.codebooks)
		if err != nil {
			t.Fatalf("loadNPYInt64Rows(%dx%d): %v", tt.codebooks, tt.frames, err)
		}
		if len(got) != tt.codebooks || len(got[0]) != tt.frames {
			t.Fatalf("read back %dx%d, want %dx%d", len(got), len(got[0]), tt.codebooks, tt.frames)
		}
		for r := range want {
			for c := range want[r] {
				if got[r][c] != want[r][c] {
					t.Fatalf("[%d][%d] = %d, want %d", r, c, got[r][c], want[r][c])
				}
			}
		}
	}
}

func TestWriteNPYInt64RowsRejectsRaggedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codes.npy")
	err := writeNPYInt64Rows(path, [][]int64{{1, 2, 3}, {1, 2}})
	if err == nil || !strings.Contains(err.Error(), "columns") {
		t.Fatalf("writeNPYInt64Rows(ragged) error = %v, want a column mismatch", err)
	}
}

// newVoiceResolver builds the part of the provider that resolves voices, with
// no ONNX sessions: resolution reads the store and the packaged reference, and
// never touches a graph.
func newVoiceResolver(t *testing.T, modelsDir, defaultVoice string) *Audio8TTS {
	t.Helper()
	return &Audio8TTS{
		man:          &audio8Manifest{ModelFingerprint: testFingerprint, NumCodebooks: 10},
		modelsDir:    modelsDir,
		defaultVoice: audio8VoiceOrDefault(defaultVoice),
		voices:       map[string]*audio8Voice{},
		prefixes:     map[string]*audio8Prefix{},
		refCodes:     testCodes(10, 110),
		refText:      "the packaged reference line",
	}
}

func TestResolveVoice(t *testing.T) {
	modelsDir := t.TempDir()
	if err := SaveAudio8Voice(modelsDir, testMeta("alice"), testCodes(10, 42)); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		defaultVoice string
		request      string
		wantName     string
		wantFrames   int
		wantErr      string
	}{
		{name: "an unknown voice is refused", request: "nobody", wantErr: "no such registered voice"},
		{name: "empty falls back to the packaged reference", wantName: config.Audio8PackagedVoice, wantFrames: 110},
		{name: "empty honours the configured default", defaultVoice: "alice", wantName: "alice", wantFrames: 42},
		{name: "a request overrides the default", defaultVoice: "alice", request: config.Audio8PackagedVoice,
			wantName: config.Audio8PackagedVoice, wantFrames: 110},
		{name: "a registered voice is loaded", request: "alice", wantName: "alice", wantFrames: 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newVoiceResolver(t, modelsDir, tt.defaultVoice)
			v, err := a.resolveVoice(tt.request)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveVoice(%q) error = %v, want %q", tt.request, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveVoice(%q): %v", tt.request, err)
			}
			if v.name != tt.wantName {
				t.Errorf("name = %q, want %q", v.name, tt.wantName)
			}
			if len(v.codes[0]) != tt.wantFrames {
				t.Errorf("frames = %d, want %d", len(v.codes[0]), tt.wantFrames)
			}
			if v.transcript == "" {
				t.Error("resolved voice has no transcript; the prompt would lose its reference text")
			}
		})
	}
}

func TestResolveVoiceWithoutAStore(t *testing.T) {
	a := newVoiceResolver(t, t.TempDir(), "")
	a.modelsDir = ""
	if _, err := a.resolveVoice(config.Audio8PackagedVoice); err != nil {
		t.Fatalf("the packaged voice must resolve without a store: %v", err)
	}
	_, err := a.resolveVoice("alice")
	if err == nil || !strings.Contains(err.Error(), "no voice store") {
		t.Fatalf("resolveVoice() error = %v, want it to say there is no store", err)
	}
}

// TestResolveVoiceIsRaceFree exercises the per-voice cache from many goroutines:
// the map behind it is shared with the prefix cache and the sessions, and a torn
// read there would be a voice speaking with another voice's codes.
func TestResolveVoiceIsRaceFree(t *testing.T) {
	modelsDir := t.TempDir()
	names := []string{"alice", "bob", "carol"}
	for i, name := range names {
		if err := SaveAudio8Voice(modelsDir, testMeta(name), testCodes(10, 10+i)); err != nil {
			t.Fatal(err)
		}
	}
	a := newVoiceResolver(t, modelsDir, "")

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := append(names, config.Audio8PackagedVoice)[i%(len(names)+1)]
			v, err := a.resolveVoice(want)
			if err != nil {
				errs <- err
				return
			}
			if v.name != want {
				errs <- errors.New("resolved " + v.name + " for " + want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(a.voices) != len(names)+1 {
		t.Errorf("cache holds %d voices, want %d", len(a.voices), len(names)+1)
	}
}

// TestSaveAudio8VoiceKeepsTheOldVoiceWhenTheInstallFails is the reviewer's
// scenario: the replacement rename fails after the previous voice has been
// moved aside. Deleting the old voice first, as this originally did, would have
// left the name with no voice at all - and the staging cleanup would then take
// the replacement too. This is the one asset under models_dir that cannot be
// re-downloaded.
func TestSaveAudio8VoiceKeepsTheOldVoiceWhenTheInstallFails(t *testing.T) {
	dir := t.TempDir()
	original := testMeta("alice")
	original.Transcript = "the original reference line"
	if err := SaveAudio8Voice(dir, original, testCodes(10, 11)); err != nil {
		t.Fatal(err)
	}

	// Fail only the install rename, leaving the set-aside rename and the
	// restore to succeed: that is the window where the voice would otherwise
	// not exist at all.
	real := audio8Rename
	t.Cleanup(func() { audio8Rename = real })
	target := config.Audio8VoiceDir(dir, "alice")
	failed := false
	audio8Rename = func(from, to string) error {
		if to == target && !failed {
			failed = true
			return errors.New("simulated rename failure")
		}
		return real(from, to)
	}

	replacement := testMeta("alice")
	replacement.Transcript = "the replacement reference line"
	err := SaveAudio8Voice(dir, replacement, testCodes(10, 99))
	if err == nil {
		t.Fatal("SaveAudio8Voice() error = nil, want the simulated failure")
	}

	audio8Rename = real
	if !failed {
		t.Fatal("the install rename was never attempted; the test proves nothing")
	}
	meta, loadErr := LoadAudio8VoiceMeta(dir, "alice")
	if loadErr != nil {
		t.Fatalf("the previous voice did not survive a failed install: %v", loadErr)
	}
	if meta.Transcript != original.Transcript || meta.Frames != 11 {
		t.Errorf("surviving voice = %q / %d frames, want the original", meta.Transcript, meta.Frames)
	}
	codes, _, err := loadAudio8VoiceCodes(dir, "alice", testFingerprint, 10)
	if err != nil {
		t.Fatalf("the surviving voice has no readable codes: %v", err)
	}
	if len(codes[0]) != 11 {
		t.Errorf("surviving codes hold %d frames, want the original 11", len(codes[0]))
	}
	// And no staging or set-aside directory is left behind.
	entries, err := os.ReadDir(config.Audio8VoicesDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "alice" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("store holds %v, want just the surviving voice", names)
	}
}

// A first registration has nothing to set aside; the same path must still work.
func TestSaveAudio8VoiceInstallsAFirstVoice(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAudio8Voice(dir, testMeta("alice"), testCodes(10, 5)); err != nil {
		t.Fatalf("SaveAudio8Voice: %v", err)
	}
	entries, err := os.ReadDir(config.Audio8VoicesDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("store holds %d entries after a first registration, want 1", len(entries))
	}
}

// TestAudio8VoiceJSONAlwaysRecordsConsent pins the on-disk shape. `omitempty`
// on the bool dropped `false`, so the consent record the feature is built
// around was simply absent for exactly the voices that need it stated.
func TestAudio8VoiceJSONAlwaysRecordsConsent(t *testing.T) {
	tests := []struct {
		name       string
		provenance Audio8VoiceProvenance
		want       string
	}{
		{
			name: "synthetic audio records that nobody consented",
			provenance: Audio8VoiceProvenance{
				Kind: Audio8VoiceSynthetic, BootstrapProvider: "kokoro", BootstrapVoice: "af_heart",
			},
			want: `"consent": false`,
		},
		{
			name:       "a recording records that someone did",
			provenance: Audio8VoiceProvenance{Kind: Audio8VoiceRecorded, SourceFile: "ref.wav", Consent: true},
			want:       `"consent": true`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			meta := testMeta("alice")
			meta.Provenance = tt.provenance
			if err := SaveAudio8Voice(dir, meta, testCodes(10, 4)); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(config.Audio8VoiceDir(dir, "alice"), config.Audio8VoiceMetaFile))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), tt.want) {
				t.Errorf("voice.json does not contain %s:\n%s", tt.want, raw)
			}
		})
	}
}

// And when even the restore fails, the previous voice is left on disk rather
// than cleaned up: at that point the set-aside copy is the only one there is.
func TestSaveAudio8VoicePreservesTheOldVoiceWhenTheRestoreAlsoFails(t *testing.T) {
	dir := t.TempDir()
	original := testMeta("alice")
	original.Transcript = "the original reference line"
	if err := SaveAudio8Voice(dir, original, testCodes(10, 11)); err != nil {
		t.Fatal(err)
	}

	real := audio8Rename
	t.Cleanup(func() { audio8Rename = real })
	target := config.Audio8VoiceDir(dir, "alice")
	audio8Rename = func(from, to string) error {
		if to == target { // every attempt to occupy the name fails
			return errors.New("simulated rename failure")
		}
		return real(from, to)
	}

	err := SaveAudio8Voice(dir, testMeta("alice"), testCodes(10, 99))
	audio8Rename = real
	if err == nil {
		t.Fatal("SaveAudio8Voice() error = nil, want the simulated failure")
	}
	if !strings.Contains(err.Error(), "preserved at") {
		t.Fatalf("error = %q, want it to name where the previous voice was kept", err)
	}

	// The message has to be true: the codes are still readable at that path.
	var preserved string
	for _, field := range strings.Fields(err.Error()) {
		if strings.Contains(field, ".old.") {
			preserved = strings.TrimSuffix(field, ")")
		}
	}
	if preserved == "" {
		t.Fatalf("could not find the preserved path in %q", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(preserved, config.Audio8VoiceMetaFile))
	if readErr != nil {
		t.Fatalf("the preserved voice is not at the path the error names: %v", readErr)
	}
	if !strings.Contains(string(raw), original.Transcript) {
		t.Errorf("the preserved voice is not the original:\n%s", raw)
	}
}

// TestAudio8VoiceNamesAreCanonicalEndToEnd is the whitespace hole: the
// validator checked the trimmed name while every caller built its path from the
// raw one, so `voices register " bob "` created a directory called " bob ",
// listed it, and let Settings select it — and then synthesis failed forever,
// because resolveVoice trims the request and asked for "bob".
//
// The rule is that a padded name registers as the trimmed one and speaks, since
// trimmed is the only spelling the request side can ever produce.
func TestAudio8VoiceNamesAreCanonicalEndToEnd(t *testing.T) {
	modelsDir := t.TempDir()
	padded := testMeta("  bob  ")
	if err := SaveAudio8Voice(modelsDir, padded, testCodes(10, 12)); err != nil {
		t.Fatalf("SaveAudio8Voice: %v", err)
	}

	// One directory, canonically named.
	entries, err := os.ReadDir(config.Audio8VoicesDir(modelsDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "bob" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("store holds %q, want exactly [bob]", names)
	}
	// The record inside agrees with the directory.
	meta, err := LoadAudio8VoiceMeta(modelsDir, "bob")
	if err != nil {
		t.Fatalf("the canonical name does not load: %v", err)
	}
	if meta.Name != "bob" {
		t.Errorf("voice.json name = %q, want bob", meta.Name)
	}
	// And the padded spelling reaches the same voice rather than a second one.
	if _, err := LoadAudio8VoiceMeta(modelsDir, " bob\n"); err != nil {
		t.Errorf("the padded spelling does not resolve to the same voice: %v", err)
	}
	if names := config.Audio8RegisteredVoiceNames(modelsDir); strings.Join(names, ",") != "bob" {
		t.Errorf("listing = %v, want [bob]", names)
	}

	// The point of the whole thing: it can be spoken. resolveVoice is the path
	// synthesis takes, and it is what used to come up empty.
	a := newVoiceResolver(t, modelsDir, "")
	for _, requested := range []string{"bob", "  bob  ", "bob\n"} {
		v, err := a.resolveVoice(requested)
		if err != nil {
			t.Errorf("resolveVoice(%q) = %v; a registered voice that cannot be resolved can never speak", requested, err)
			continue
		}
		if v.name != "bob" || len(v.codes[0]) != 12 {
			t.Errorf("resolveVoice(%q) = %q with %d frames, want bob with 12", requested, v.name, len(v.codes[0]))
		}
	}

	// Re-registering the trimmed spelling replaces it rather than making a
	// second directory that shadows the first.
	second := testMeta("bob")
	second.Transcript = "the replacement line"
	if err := SaveAudio8Voice(modelsDir, second, testCodes(10, 33)); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(config.Audio8VoicesDir(modelsDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("store holds %d entries after re-registering the padded name, want 1", len(entries))
	}

	// Removal takes the padded spelling too.
	if err := RemoveAudio8Voice(modelsDir, " bob "); err != nil {
		t.Errorf("RemoveAudio8Voice(\" bob \") = %v, want it to reach the same voice", err)
	}
	if names := config.Audio8RegisteredVoiceNames(modelsDir); len(names) != 0 {
		t.Errorf("after removal the store lists %v", names)
	}
}

// TestRegisterAudio8VoiceRefusesAnExistingName covers the guard where it lives.
// The CLI test that used to claim this faked RegisterAudio8Voice out entirely,
// so deleting the guard passed.
//
// No export is needed: the check runs before NewAudio8Encoder, and the point of
// the ordering is that a name collision is reported without paying for ~400 MB
// of weights. The error each call returns says which of the two ran.
func TestRegisterAudio8VoiceRefusesAnExistingName(t *testing.T) {
	modelsDir := t.TempDir()
	if err := SaveAudio8Voice(modelsDir, testMeta("alice"), testCodes(10, 8)); err != nil {
		t.Fatal(err)
	}
	req := Audio8RegisterRequest{
		ModelDir:   t.TempDir(), // deliberately not an export
		ModelsDir:  modelsDir,
		Name:       "alice",
		Transcript: "a new reference line",
		Samples:    make([]float32, 44100),
		SampleRate: 44100,
	}

	_, err := RegisterAudio8Voice(context.Background(), req)
	if err == nil {
		t.Fatal("RegisterAudio8Voice() error = nil, want a name collision")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %q, want the collision refused before the encoder loads", err)
	}

	// A padded spelling is the same voice, so it must collide too.
	padded := req
	padded.Name = "  alice  "
	if _, err := RegisterAudio8Voice(context.Background(), padded); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Errorf("RegisterAudio8Voice(%q) error = %v, want the same collision", padded.Name, err)
	}

	// With --overwrite the guard stands aside, and the next thing to fail is
	// the export that is not there - which is how we know the guard is what
	// stopped the previous calls.
	req.Overwrite = true
	_, err = RegisterAudio8Voice(context.Background(), req)
	if err == nil {
		t.Fatal("RegisterAudio8Voice(--overwrite) error = nil, want the missing export")
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Fatalf("--overwrite did not lift the guard: %v", err)
	}
	if !strings.Contains(err.Error(), "runtime_manifest.json") {
		t.Errorf("error = %q, want the missing export, i.e. execution reached the encoder", err)
	}
	// And the existing voice is untouched by the failed overwrite.
	if _, err := LoadAudio8VoiceMeta(modelsDir, "alice"); err != nil {
		t.Errorf("a failed overwrite damaged the existing voice: %v", err)
	}
}

// stubPrefix is a cache entry with no recurrent state: the cache does not read
// it, and allocating a real one per voice would be 55 MB a time.
func stubPrefix(length int) *audio8Prefix {
	return &audio8Prefix{state: &slowState{}, length: length}
}

// cachedNames reports the cache contents, least recently used first.
func cachedNames(a *Audio8TTS) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.prefixLRU))
	copy(out, a.prefixLRU)
	return out
}

func insertPrefix(a *Audio8TTS, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.insertPrefixLocked(name, stubPrefix(len(name)))
}

func lookupPrefix(a *Audio8TTS, name string) *audio8Prefix {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lookupPrefixLocked(name)
}

// TestAudio8PrefixCacheEvictsLeastRecentlyUsed pins the eviction order. Before
// this the cache was unbounded, and at 55.4 MB an entry that is a leak the size
// of the model once a user can register voices freely.
func TestAudio8PrefixCacheEvictsLeastRecentlyUsed(t *testing.T) {
	a := newVoiceResolver(t, t.TempDir(), "")

	for _, name := range []string{"one", "two", "three"} {
		insertPrefix(a, name)
	}
	if got := strings.Join(cachedNames(a), ","); got != "one,two,three" {
		t.Fatalf("cache = %s, want the three inserted in order", got)
	}

	// A hit on the oldest entry makes it the newest, so the next eviction takes
	// what is now least recently used.
	if lookupPrefix(a, "one") == nil {
		t.Fatal("lookupPrefixLocked(one) = nil, want the entry just inserted")
	}
	if got := strings.Join(cachedNames(a), ","); got != "two,three,one" {
		t.Fatalf("cache = %s, want a hit to move the entry to the newest end", got)
	}

	insertPrefix(a, "four")
	if got := strings.Join(cachedNames(a), ","); got != "three,one,four" {
		t.Fatalf("cache = %s, want two evicted as least recently used", got)
	}
	if lookupPrefix(a, "two") != nil {
		t.Error("the evicted entry is still cached")
	}
	if len(a.prefixes) != audio8PrefixCacheSize {
		t.Errorf("cache holds %d entries, want the bound of %d", len(a.prefixes), audio8PrefixCacheSize)
	}

	// Re-inserting an evicted voice is the re-prime path, and must not grow the
	// cache past the bound.
	insertPrefix(a, "two")
	if len(a.prefixes) != audio8PrefixCacheSize || len(a.prefixLRU) != audio8PrefixCacheSize {
		t.Errorf("after a re-prime: %d entries, %d LRU slots, want %d of each",
			len(a.prefixes), len(a.prefixLRU), audio8PrefixCacheSize)
	}

	// Re-inserting a voice already cached replaces rather than duplicating it.
	insertPrefix(a, "two")
	seen := map[string]int{}
	for _, n := range cachedNames(a) {
		seen[n]++
	}
	if seen["two"] != 1 {
		t.Errorf("cache lists 'two' %d times, want once", seen["two"])
	}
	if len(a.prefixLRU) != len(a.prefixes) {
		t.Errorf("LRU (%d) and map (%d) disagree", len(a.prefixLRU), len(a.prefixes))
	}
}

// TestAudio8PrefixCacheIsRaceFree hammers the cache from many goroutines with
// more voices than the bound. The map and the LRU slice are mutated together
// under one lock; a torn update there would be a voice speaking with another
// voice's primed state.
func TestAudio8PrefixCacheIsRaceFree(t *testing.T) {
	a := newVoiceResolver(t, t.TempDir(), "")
	names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}

	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := names[i%len(names)]
			if p := lookupPrefix(a, name); p == nil {
				insertPrefix(a, name)
			}
		}(i)
	}
	wg.Wait()

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.prefixes) > audio8PrefixCacheSize {
		t.Errorf("cache holds %d entries, want at most %d", len(a.prefixes), audio8PrefixCacheSize)
	}
	if len(a.prefixLRU) != len(a.prefixes) {
		t.Errorf("LRU holds %d names but the map holds %d entries", len(a.prefixLRU), len(a.prefixes))
	}
	// Every LRU name resolves, and no name appears twice.
	seen := map[string]bool{}
	for _, n := range a.prefixLRU {
		if seen[n] {
			t.Errorf("%q appears twice in the LRU", n)
		}
		seen[n] = true
		if a.prefixes[n] == nil {
			t.Errorf("LRU names %q but the map has no entry for it", n)
		}
	}
}

// TestAudio8PrefixEntrySizeMatchesTheDocumentedBound checks the arithmetic in
// audio8PrefixCacheSize's comment against what the allocator actually produces
// for the shipped export's dimensions. A bound justified by a number that has
// drifted is not a justified bound.
func TestAudio8PrefixEntrySizeMatchesTheDocumentedBound(t *testing.T) {
	shipped := &audio8Manifest{NumLayers: 24, NLocalHeads: 2, MaxSeqLen: 2048, HeadDim: 64}
	entry := &audio8Prefix{state: newSlowState(shipped)}

	// 2 KV caches of 24*2*2048*64 float32, plus SSM and conv state.
	const want = 2*24*2*2048*64*4 + 24*24*32*64*4 + 24*896*4*4
	if got := entry.bytes(); got != want {
		t.Fatalf("one primed prefix is %d bytes, the comment says %d", got, want)
	}
	if want != 55394304 {
		t.Fatalf("the documented 55.4 MB per entry is now %d bytes", want)
	}
	t.Logf("one primed prefix = %.1f MB; the cache is bounded at %d = %.0f MB",
		float64(want)/(1<<20), audio8PrefixCacheSize,
		float64(want)*audio8PrefixCacheSize/(1<<20))
}
