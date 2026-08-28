package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeMeetingDiarizer(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr string
	}{
		{name: "unknown value is refused", value: "pyannote", wantErr: "unsupported speaker.meeting.diarizer"},
		{name: "a near miss is refused too", value: "sortformer-v2", wantErr: "choose one of"},
		{name: "unset is the default", value: "", want: DiarizerSherpa},
		{name: "sherpa", value: "sherpa", want: DiarizerSherpa},
		{name: "sortformer", value: "sortformer", want: DiarizerSortformer},
		{name: "case and padding are tolerated", value: "  SORTFORMER ", want: DiarizerSortformer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeMeetingDiarizer(tt.value)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("= %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMeetingDiarizerDefaultsToSherpa(t *testing.T) {
	loadIntoEmptyInstall(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Speaker.Meeting.Diarizer != DiarizerSherpa {
		t.Fatalf("speaker.meeting.diarizer = %q, want the sherpa default", cfg.Speaker.Meeting.Diarizer)
	}
	if got, err := MeetingDiarizerFrom(cfg); err != nil || got != DiarizerSherpa {
		t.Fatalf("MeetingDiarizerFrom() = %q, %v, want sherpa", got, err)
	}
	if got, err := MeetingDiarizerFrom(nil); err != nil || got != DiarizerSherpa {
		t.Fatalf("MeetingDiarizerFrom(nil) = %q, %v, want sherpa", got, err)
	}
}

// installSortformerGGUF writes the managed diarizer weights into a fresh
// models dir.
func installSortformerGGUF(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := SortformerDiarizerAsset().ModelPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sortformerConfig(diarizer string) *Config {
	cfg := &Config{
		STTProvider:         "sherpa-parakeet",
		TTSProvider:         "kokoro",
		TranscribeCPPBinary: "transcribe-cli",
	}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	cfg.Speaker.Meeting.Diarizer = diarizer
	return cfg
}

func TestDiagnoseMeetingDiarizer(t *testing.T) {
	installed := installSortformerGGUF(t)
	gpuLookPath := fakeDeviceCLI(t, gpuDeviceList)
	cpuLookPath := fakeDeviceCLI(t, cpuOnlyDeviceList)

	tests := []struct {
		name         string
		diarizer     string
		numSpeakers  int
		modelsDir    string
		lookPath     func(string) (string, error)
		wantRow      string
		wantSeverity Severity
		wantDetail   string
	}{
		{
			name: "unknown engine", diarizer: "pyannote", modelsDir: installed, lookPath: gpuLookPath,
			wantRow: "meeting-diarizer", wantSeverity: SeverityError, wantDetail: "unsupported speaker.meeting.diarizer",
		},
		{
			name: "missing binary", diarizer: DiarizerSortformer, modelsDir: installed, lookPath: failLookPath,
			wantRow: "sortformer-diarizer", wantSeverity: SeverityError, wantDetail: "not found on PATH",
		},
		{
			name: "missing gguf", diarizer: DiarizerSortformer, modelsDir: t.TempDir(), lookPath: okLookPath,
			wantRow: "sortformer-diarizer", wantSeverity: SeverityError, wantDetail: "GGUF is missing",
		},
		{
			name: "binary cannot list devices", diarizer: DiarizerSortformer, modelsDir: installed, lookPath: okLookPath,
			wantRow: "sortformer-diarizer", wantSeverity: SeverityError, wantDetail: "--list-devices",
		},
		{
			// Unlike the STT provider, a CPU-only machine is fine here:
			// Sortformer on CPU still beats the sherpa review path (T3).
			name: "cpu-only machine is usable", diarizer: DiarizerSortformer, modelsDir: installed, lookPath: cpuLookPath,
			wantRow: "sortformer-diarizer", wantSeverity: SeverityOK, wantDetail: "backends: cpu",
		},
		{
			name: "installed", diarizer: DiarizerSortformer, modelsDir: installed, lookPath: gpuLookPath,
			wantRow: "sortformer-diarizer", wantSeverity: SeverityOK, wantDetail: "backends: metal, cpu",
		},
		{
			name: "expected speakers is ignored, not silently dropped", diarizer: DiarizerSortformer,
			numSpeakers: 3, modelsDir: installed, lookPath: gpuLookPath,
			wantRow: "sortformer-expected-speakers", wantSeverity: SeverityWarn, wantDetail: "no speaker-count input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sortformerConfig(tt.diarizer)
			cfg.Speaker.Meeting.NumSpeakers = tt.numSpeakers
			d := diagByName(Diagnose(cfg, tt.modelsDir, tt.lookPath))[tt.wantRow]
			if d.Severity != tt.wantSeverity {
				t.Fatalf("%s = %+v, want severity %s", tt.wantRow, d, tt.wantSeverity)
			}
			if !strings.Contains(d.Detail, tt.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", d.Detail, tt.wantDetail)
			}
			if tt.wantSeverity != SeverityOK && d.Remediation == "" {
				t.Error("a non-OK diagnostic must carry remediation")
			}
		})
	}
}

// The settings row reads the same install state doctor does, without paying
// for the backend probe.
func TestMeetingDiarizerGap(t *testing.T) {
	installed := installSortformerGGUF(t)

	tests := []struct {
		name       string
		diarizer   string
		modelsDir  string
		lookPath   func(string) (string, error)
		wantDetail string
	}{
		{name: "sherpa needs nothing", diarizer: DiarizerSherpa, modelsDir: t.TempDir(), lookPath: failLookPath},
		{name: "unset needs nothing", diarizer: "", modelsDir: t.TempDir(), lookPath: failLookPath},
		{name: "an unknown engine is not an install gap", diarizer: "pyannote", modelsDir: t.TempDir(), lookPath: failLookPath},
		{
			name: "missing CLI", diarizer: DiarizerSortformer, modelsDir: installed, lookPath: failLookPath,
			wantDetail: TranscribeCPPCLIName + " not installed",
		},
		{
			name: "missing weights", diarizer: DiarizerSortformer, modelsDir: t.TempDir(), lookPath: okLookPath,
			wantDetail: "weights not downloaded",
		},
		{name: "installed", diarizer: DiarizerSortformer, modelsDir: installed, lookPath: okLookPath},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gap := MeetingDiarizerGap(sortformerConfig(tt.diarizer), tt.modelsDir, tt.lookPath)
			if gap.Detail != tt.wantDetail {
				t.Fatalf("detail = %q, want %q", gap.Detail, tt.wantDetail)
			}
			if gap.Missing() && gap.Remediation == "" {
				t.Errorf("a reported gap must carry remediation: %+v", gap)
			}
		})
	}

	// No config and no lookPath are the picker's own zero values, not a gap.
	if gap := MeetingDiarizerGap(nil, installed, okLookPath); gap.Missing() {
		t.Errorf("nil config = %+v, want no gap", gap)
	}
	if gap := MeetingDiarizerGap(sortformerConfig(DiarizerSortformer), t.TempDir(), nil); gap.Missing() {
		t.Errorf("nil lookPath = %+v, want no gap", gap)
	}
}

// doctor and 'models ensure' must agree: a row telling the user to fetch
// weights the plan will never fetch is a permanent, unfixable FAIL.
func TestDiagnoseAndEnsureAgreeOnWhenSortformerIsNeeded(t *testing.T) {
	tests := []struct {
		name    string
		speaker bool
		meeting bool
		live    bool
		want    bool // doctor reports AND models ensure plans
	}{
		{name: "speaker on, meetings on", speaker: true, meeting: true, want: true},
		{name: "speaker on, meetings off", speaker: true, meeting: false, live: true, want: false},
		{name: "speaker off entirely", speaker: false, meeting: false, live: false, want: false},
	}

	assetID := SortformerManifestAsset().ID
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sortformerConfig(DiarizerSortformer)
			cfg.Speaker.Enabled = tt.speaker
			cfg.Speaker.Meeting.Enabled = tt.meeting
			cfg.Speaker.Live.Enabled = tt.live

			// An empty models dir: the GGUF is missing in every combination, so
			// the only thing that varies is whether either surface asks for it.
			reported := false
			for _, d := range Diagnose(cfg, t.TempDir(), fakeDeviceCLI(t, gpuDeviceList)) {
				if d.Name == "sortformer-diarizer" {
					reported = true
					if d.Severity != SeverityError {
						t.Fatalf("expected the missing GGUF to be an error, got %+v", d)
					}
				}
			}

			m, err := ManifestFor(cfg, DefaultAssetRequest(cfg))
			if err != nil {
				t.Fatalf("ManifestFor() error = %v", err)
			}
			planned := false
			for _, a := range m.Assets {
				if a.ID == assetID {
					planned = true
				}
			}

			if reported != tt.want || planned != tt.want {
				t.Fatalf("doctor reports = %v, models ensure plans = %v, want both %v",
					reported, planned, tt.want)
			}
		})
	}
}

// The claim is protected wider than it is planned — deliberately, per
// required_claims.go: turning meetings off must not make 139 MB of already
// downloaded weights "unused" and offer them to 'models clean'.
func TestSortformerGGUFStaysClaimedWhenMeetingsAreOff(t *testing.T) {
	cfg := sortformerConfig(DiarizerSortformer)
	cfg.Speaker.Meeting.Enabled = false
	modelsDir := t.TempDir()

	want := SortformerDiarizerAsset().ModelPath(modelsDir)
	for _, claim := range speakerConfigClaims(cfg, modelsDir) {
		for _, own := range claim.own {
			if own == want {
				return
			}
		}
	}
	t.Fatal("the sortformer GGUF lost its claim when meetings were switched off")
}

func TestDiagnoseDefaultDiarizerNeverMentionsSortformer(t *testing.T) {
	cfg := sortformerConfig("")
	for _, d := range Diagnose(cfg, t.TempDir(), failLookPath) {
		if strings.Contains(d.Name, "sortformer") || strings.Contains(strings.ToLower(d.Detail), "sortformer") {
			t.Errorf("the default diarizer doctor mentions the optional engine: %+v", d)
		}
	}
}

func TestSortformerGGUFIsPlannedAndProtectedOnlyWhenSelected(t *testing.T) {
	assetID := SortformerManifestAsset().ID

	for _, tt := range []struct {
		diarizer string
		want     bool
	}{
		{diarizer: "", want: false},
		{diarizer: DiarizerSherpa, want: false},
		{diarizer: DiarizerSortformer, want: true},
	} {
		t.Run("diarizer="+tt.diarizer, func(t *testing.T) {
			cfg := sortformerConfig(tt.diarizer)
			m, err := ManifestFor(cfg, AssetRequest{NeedSpeaker: true})
			if err != nil {
				t.Fatalf("ManifestFor() error = %v", err)
			}
			planned := false
			for _, a := range m.Assets {
				if a.ID == assetID {
					planned = true
				}
			}
			if planned != tt.want {
				t.Errorf("sortformer GGUF planned = %v, want %v", planned, tt.want)
			}

			// A planned asset must also be claimed, or 'models clean' offers to
			// delete weights the configured diarizer needs.
			modelsDir := t.TempDir()
			claimed := false
			for _, claim := range speakerConfigClaims(cfg, modelsDir) {
				for _, own := range claim.own {
					if own == SortformerDiarizerAsset().ModelPath(modelsDir) {
						claimed = true
					}
				}
			}
			if claimed != tt.want {
				t.Errorf("sortformer GGUF claimed = %v, want %v", claimed, tt.want)
			}
		})
	}
}

func TestManifestRefusesAnUnknownDiarizer(t *testing.T) {
	if _, err := ManifestFor(sortformerConfig("pyannote"), AssetRequest{NeedSpeaker: true}); err == nil {
		t.Fatal("ManifestFor() error = nil, want an unknown diarizer to fail the plan")
	}
}
