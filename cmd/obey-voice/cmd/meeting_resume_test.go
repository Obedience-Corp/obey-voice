//go:build !integration

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
)

func plantResumeBundle(t *testing.T, dir, id string, lines []string) string {
	t.Helper()
	bundle := filepath.Join(dir, id)
	internal := filepath.Join(bundle, meetinglog.BundleInternalDirName)
	if err := os.MkdirAll(internal, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(internal, meetinglog.BundleEventsName),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, meetinglog.BundleDocumentName),
		[]byte("# Meeting: Standup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return bundle
}

// A bad --resume must be rejected from the reference alone: runMeetingRecord
// resolves it before the asset preflight so a typo never costs a model
// download and a hot microphone.
func TestResolveMeetingResumeRejectsAnUnknownReference(t *testing.T) {
	if _, err := resolveMeetingResume(context.Background(), t.TempDir(), "nope"); err == nil {
		t.Fatal("expected an error for an unknown resume reference")
	}
	target, err := resolveMeetingResume(context.Background(), t.TempDir(), "")
	if err != nil || target != nil {
		t.Fatalf("no --resume should resolve to (nil, nil), got %+v %v", target, err)
	}
}

// Route durability on resume keys off the plan the bundle already carries, not
// off the fact that this is a resume: --resume last --route docs still gets a
// durable plan the startup sweep can retry.
func TestResolveMeetingResumeReportsExistingRoutePlan(t *testing.T) {
	dir := t.TempDir()
	plantResumeBundle(t, dir, "planning-20260722-090000.meeting", []string{
		`{"type":"session_start","ts":"2026-07-22T09:00:00Z","offset_ms":0,"description":"Planning"}`,
		`{"type":"session_end","ts":"2026-07-22T09:30:00Z","offset_ms":1800000}`,
	})

	target, err := resolveMeetingResume(context.Background(), dir, "last")
	if err != nil {
		t.Fatal(err)
	}
	if target.hasRoutePlan || target.alreadyRouted {
		t.Fatalf("unrouted bundle reported plan=%v routed=%v", target.hasRoutePlan, target.alreadyRouted)
	}

	planned := t.TempDir()
	plantResumeBundle(t, planned, "planning-20260722-090000.meeting", []string{
		`{"type":"session_start","ts":"2026-07-22T09:00:00Z","offset_ms":0,"description":"Planning"}`,
		`{"type":"route_plan","ts":"2026-07-22T09:00:01Z","offset_ms":1000,"label":"docs","text":"auto"}`,
		`{"type":"session_end","ts":"2026-07-22T09:30:00Z","offset_ms":1800000}`,
		`{"type":"routed","ts":"2026-07-22T09:30:05Z","offset_ms":1805000,"label":"docs","text":"file","message":"delivered: docs"}`,
	})
	target, err = resolveMeetingResume(context.Background(), planned, "last")
	if err != nil {
		t.Fatal(err)
	}
	if !target.hasRoutePlan || !target.alreadyRouted {
		t.Fatalf("routed bundle reported plan=%v routed=%v", target.hasRoutePlan, target.alreadyRouted)
	}
}
