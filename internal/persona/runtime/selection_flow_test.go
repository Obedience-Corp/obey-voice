package personaruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSelectionFlowInfo(t *testing.T) {
	version, gotHash, err := SelectionFlowInfo()
	if err != nil {
		t.Fatal(err)
	}
	data, err := selectionFlowBytes.ReadFile(selectionFlowPath)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(data)
	if version != supportedSelectionFlowVersion || gotHash != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("SelectionFlowInfo() = (%d, %q), want (%d, %q)", version, gotHash, supportedSelectionFlowVersion, hex.EncodeToString(wantDigest[:]))
	}
}

func TestRunSelectionFlowCallbackOrder(t *testing.T) {
	var got []string
	err := RunSelectionFlow(context.Background(), SelectionActions{
		Resolve: func(context.Context) error { got = append(got, "resolve"); return nil },
		Prepare: func(context.Context) error { got = append(got, "prepare"); return nil },
		Commit:  func(context.Context) error { got = append(got, "commit"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"resolve", "prepare", "commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("callback order = %v, want %v", got, want)
	}
}

func TestRunSelectionFlowStopsWithStageError(t *testing.T) {
	wantErr := errors.New("prepare failed")
	var got []string
	err := RunSelectionFlow(context.Background(), SelectionActions{
		Resolve: func(context.Context) error { got = append(got, "resolve"); return nil },
		Prepare: func(context.Context) error { got = append(got, "prepare"); return wantErr },
		Commit:  func(context.Context) error { got = append(got, "commit"); return nil },
	})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "prepare") {
		t.Fatalf("error = %v, want prepare stage wrapping %v", err, wantErr)
	}
	if want := []string{"resolve", "prepare"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("callback order = %v, want %v", got, want)
	}
}

func TestRunSelectionFlowContextBoundaries(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		err := RunSelectionFlow(ctx, SelectionActions{
			Resolve: func(context.Context) error { called = true; return nil },
			Prepare: func(context.Context) error { return nil },
			Commit:  func(context.Context) error { return nil },
		})
		if !errors.Is(err, context.Canceled) || called {
			t.Fatalf("error = %v, resolve called = %v", err, called)
		}
	})
	t.Run("cancel before commit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		called := []string{}
		err := RunSelectionFlow(ctx, SelectionActions{
			Resolve: func(context.Context) error { called = append(called, "resolve"); return nil },
			Prepare: func(context.Context) error { called = append(called, "prepare"); cancel(); return nil },
			Commit:  func(context.Context) error { called = append(called, "commit"); return nil },
		})
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(called, []string{"resolve", "prepare"}) {
			t.Fatalf("error = %v, callbacks = %v", err, called)
		}
	})
	t.Run("cancellation after commit succeeds", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := RunSelectionFlow(ctx, SelectionActions{
			Resolve: func(context.Context) error { return nil },
			Prepare: func(context.Context) error { return nil },
			Commit:  func(context.Context) error { cancel(); return nil },
		})
		if err != nil {
			t.Fatalf("error = %v, want nil after successful commit", err)
		}
	})
}

func TestParseSelectionFlowRejectsInvalidAssets(t *testing.T) {
	valid := `version: 1
start: start
states:
  start: {action: resolve, success: resolved, error: failed}
  resolved: {action: prepare, success: prepared, error: failed}
  prepared: {action: commit, success: committed, error: failed}
  committed: {}
  failed: {}
`
	tests := []struct {
		name string
		data string
	}{
		{name: "version", data: strings.Replace(valid, "version: 1", "version: 2", 1)},
		{name: "unknown action", data: strings.Replace(valid, "action: resolve", "action: route", 1)},
		{name: "unknown field", data: valid + "extra: true\n"},
		{name: "missing error path", data: strings.Replace(valid, ", error: failed", "", 1)},
		{name: "cycle", data: strings.Replace(valid, "success: resolved", "success: start", 1)},
		{name: "duplicate state", data: valid + "  start: {}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseSelectionFlow([]byte(tt.data)); err == nil {
				t.Fatal("parseSelectionFlow accepted invalid asset")
			}
		})
	}
}
