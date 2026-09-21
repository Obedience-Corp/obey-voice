package audio

import (
	"strings"
	"testing"
	"time"
)

func TestFileVADParams(t *testing.T) {
	cases := []struct {
		name           string
		opts           FileVADOptions
		wantMinSilence float32
		wantMaxSpeech  float32
		wantErr        string
	}{
		{
			name:           "zero options take the file defaults",
			opts:           FileVADOptions{},
			wantMinSilence: 0.6,
			wantMaxSpeech:  20,
		},
		{
			name:           "explicit options are forwarded",
			opts:           FileVADOptions{MinSilence: 900 * time.Millisecond, MaxSpeech: 15 * time.Second},
			wantMinSilence: 0.9,
			wantMaxSpeech:  15,
		},
		{
			name:    "negative silence is rejected",
			opts:    FileVADOptions{MinSilence: -time.Second},
			wantErr: "MinSilence -1s is negative",
		},
		{
			name:    "negative max speech is rejected",
			opts:    FileVADOptions{MaxSpeech: -time.Second},
			wantErr: "MaxSpeech -1s is negative",
		},
		{
			name:    "max speech past the detector buffer is rejected",
			opts:    FileVADOptions{MaxSpeech: 31 * time.Second},
			wantErr: "exceeds the detector's 30s buffer",
		},
		{
			name:    "max speech below the minimum speech duration is rejected",
			opts:    FileVADOptions{MaxSpeech: 100 * time.Millisecond},
			wantErr: "below the 250ms minimum speech duration",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fileVADParams(tc.opts)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("fileVADParams(%+v) = %+v, want an error", tc.opts, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fileVADParams: %v", err)
			}
			if got.minSilence != tc.wantMinSilence {
				t.Errorf("minSilence = %v, want %v", got.minSilence, tc.wantMinSilence)
			}
			if got.maxSpeech != tc.wantMaxSpeech {
				t.Errorf("maxSpeech = %v, want %v", got.maxSpeech, tc.wantMaxSpeech)
			}
			if got.threshold != vadThreshold {
				t.Errorf("threshold = %v, want the package default %v", got.threshold, float32(vadThreshold))
			}
			if got.minSpeech != vadMinSpeechDuration {
				t.Errorf("minSpeech = %v, want the package default %v", got.minSpeech, float32(vadMinSpeechDuration))
			}
		})
	}
}

// TestFileVADOptionsResolved pins the values a caller sizing a buffer reads.
// The span detector sizes its ring from these, so a zero option and the
// detector's own default have to be the same number.
func TestFileVADOptionsResolved(t *testing.T) {
	got := FileVADOptions{}.Resolved()
	if got.MinSilence != defaultFileMinSilence || got.MaxSpeech != defaultFileMaxSpeech {
		t.Fatalf("Resolved() = %+v, want the file defaults", got)
	}
	explicit := FileVADOptions{MinSilence: 900 * time.Millisecond, MaxSpeech: 15 * time.Second}
	if explicit.Resolved() != explicit {
		t.Fatalf("Resolved() changed explicit options: %+v", explicit.Resolved())
	}
}

// TestFileVADIgnoresLiveCaptureConfig pins the rule that the file detector is
// configured by its options, not by the keys that tune live capture.
func TestFileVADIgnoresLiveCaptureConfig(t *testing.T) {
	got, err := fileVADParams(FileVADOptions{})
	if err != nil {
		t.Fatalf("fileVADParams: %v", err)
	}
	if got.minSilence == 1.2 {
		t.Fatal("file VAD picked up the live vad_silence_duration default")
	}
}
