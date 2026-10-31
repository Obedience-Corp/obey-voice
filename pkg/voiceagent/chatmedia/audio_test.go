package chatmedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func audioFixture(samples int) []byte {
	data := make([]byte, 44+samples*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 16000)
	binary.LittleEndian.PutUint32(data[28:], 32000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(samples*2))
	for i := 44; i < len(data); i++ {
		data[i] = byte(i)
	}
	return data
}
func TestCanonicalAudioPreservesPCMAndRejectsInvalidInput(t *testing.T) {
	original := audioFixture(3)
	ref, got, err := CanonicalAudio(original)
	if err != nil {
		t.Fatal(err)
	}
	if ref.SampleCount != 3 || !bytes.Equal(got, original) {
		t.Fatal("canonical PCM changed")
	}
	for name, data := range map[string][]byte{"empty": nil, "truncated": original[:45], "oversized": make([]byte, utterance.MaxBytes+1), "too_long": audioFixture(utterance.MaxSamples + 1)} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := CanonicalAudio(data); err == nil {
				t.Fatal("accepted malformed audio")
			}
		})
	}
	stereo := bytes.Clone(original)
	binary.LittleEndian.PutUint16(stereo[22:], 2)
	if _, _, err := CanonicalAudio(stereo); err == nil {
		t.Fatal("accepted stereo")
	}
}
func TestSessionAudioOwnershipAndCancellation(t *testing.T) {
	ctx := context.Background()
	owner := NewAudioSession()
	other := NewAudioSession()
	defer other.Close()
	data := audioFixture(3)
	ref, err := owner.Import(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	data[44] = 0 // caller mutation cannot alter owned bytes.
	got, err := owner.ResolveAudio(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got[44] == 0 {
		t.Fatal("import borrowed caller bytes")
	}
	got[44] = 0
	again, _ := owner.ResolveAudio(ctx, ref)
	if again[44] == 0 {
		t.Fatal("resolver exposed owned bytes")
	}
	if _, err := other.ResolveAudio(ctx, ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal(err)
	}
	forged := ref
	forged.SampleCount++
	if _, err := owner.ResolveAudio(ctx, forged); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal(err)
	}
	if err := owner.Retain(ref); err != nil {
		t.Fatal(err)
	}
	owner.Release(ref)
	if _, err := owner.ResolveAudio(ctx, ref); err != nil {
		t.Fatal(err)
	}
	owner.Release(ref)
	if _, err := owner.ResolveAudio(ctx, ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := owner.Import(cancelled, audioFixture(3)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	owner.Close()
	if _, err := owner.Import(ctx, audioFixture(3)); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal(err)
	}
}
func TestSessionAudioBudgetDoesNotEvictRetainedTurns(t *testing.T) {
	session := NewAudioSession()
	defer session.Close()
	ctx := context.Background()
	data := audioFixture(utterance.MaxSamples)
	var first utterance.AudioRef
	for i := 0; ; i++ {
		binary.LittleEndian.PutUint32(data[44:], uint32(i))
		ref, err := session.Import(ctx, data)
		if errors.Is(err, utterance.ErrBudgetExceeded) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = ref
		}
		if i > 40 {
			t.Fatal("budget was not enforced")
		}
	}
	if _, err := session.ResolveAudio(ctx, first); err != nil {
		t.Fatalf("retained first turn evicted: %v", err)
	}
}

func TestCanonicalAudioRejectsAmbiguousChunksAndTrailingFragments(t *testing.T) {
	original := audioFixture(3)
	duplicateData := append(bytes.Clone(original), original[36:]...)
	duplicateFmt := append(bytes.Clone(original), original[12:36]...)
	trailing := append(bytes.Clone(original), 1)
	missingPad := append(bytes.Clone(original), []byte("JUNK\x01\x00\x00\x00x")...)
	for name, data := range map[string][]byte{"duplicate_data": duplicateData, "duplicate_fmt": duplicateFmt, "trailing": trailing, "missing_pad": missingPad} {
		binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
		t.Run(name, func(t *testing.T) {
			if _, _, err := CanonicalAudio(data); err == nil {
				t.Fatal("accepted ambiguous WAV")
			}
		})
	}
}
