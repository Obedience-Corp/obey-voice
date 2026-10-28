//go:build !windows

package talkhotkey

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
)

func TestParticipationControlRoundTripAndInvalidUpdate(t *testing.T) {
	controlHome(t)
	control := listenForTest(t)
	settings := discussion.DefaultSettings()
	control.SetParticipationHandler(func(ctx context.Context, next *discussion.Settings) (discussion.Settings, error) {
		if err := ctx.Err(); err != nil {
			return discussion.Settings{}, err
		}
		if next != nil {
			settings = next.Clone()
		}
		return settings.Clone(), nil
	})
	got, err := Participation(t.Context(), nil)
	if err != nil || got.Mode != "invited" {
		t.Fatalf("read=%+v err=%v", got, err)
	}
	next := discussion.Settings{Version: 1, Mode: "open", Members: []string{"jarvis", "samantha"}}
	got, err = Participation(t.Context(), &next)
	if err != nil || !reflect.DeepEqual(next, got) {
		t.Fatalf("write=%+v err=%v", got, err)
	}
	invalid := next
	invalid.Mode = "invented"
	if _, err := Participation(t.Context(), &invalid); err == nil {
		t.Fatal("invalid mode accepted")
	}
	got, err = Participation(t.Context(), nil)
	if err != nil || !reflect.DeepEqual(next, got) {
		t.Fatal("invalid update changed settings")
	}
	select {
	case event := <-control.Events():
		t.Fatalf("settings emitted a talk edge: %+v", event)
	default:
	}
}
func TestParticipationOfflineDoesNotCreateEndpoint(t *testing.T) {
	controlHome(t)
	if _, err := Participation(t.Context(), nil); !errors.Is(err, ErrNoListener) {
		t.Fatalf("offline=%v", err)
	}
}
