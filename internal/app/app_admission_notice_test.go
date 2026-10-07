package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestClassifyVoiceTurnFailureShutsDownOnParentAdmissionCancellationCategory(t *testing.T) {
	err := &pipeline.AdmissionError{
		Err: admission.WrapFailure(
			admission.FailureParentCancellation,
			"route",
			errors.New("canceled during admission"),
		),
	}
	if got := ClassifyVoiceTurnFailure(err, nil, "Veronica", 1); got != VoiceShutdown {
		t.Fatalf("classified=%d want shutdown", got)
	}
}

func TestBuildAdmissionRetryNoticeClassifiesFailures(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		input    string
		wantCat  admission.FailureCategory
		wantText string
	}{
		{
			name: "identity refusal",
			err: &pipeline.AdmissionError{
				Err: admission.WrapFailure(admission.FailureIdentityRefusal, "route", errors.New("persona is ambiguous")),
			},
			input:    "Santa what do you think",
			wantCat:  admission.FailureIdentityRefusal,
			wantText: "identity refusal",
		},
		{
			name: "classifier unavailable",
			err: &pipeline.AdmissionError{
				Err: admission.WrapFailure(admission.FailureClassifierUnavailable, "route", errors.New("offline")),
			},
			input:    "Santa what do you think",
			wantCat:  admission.FailureClassifierUnavailable,
			wantText: "classifier unavailable",
		},
		{
			name: "classifier timeout",
			err: &pipeline.AdmissionError{
				Err: admission.WrapFailure(admission.FailureClassifierLocalTimeout, "route", errors.New("timeout")),
			},
			input:    "Santa what do you think",
			wantCat:  admission.FailureClassifierLocalTimeout,
			wantText: "classifier timeout",
		},
		{
			name: "classifier invalid output",
			err: &pipeline.AdmissionError{
				Err: admission.WrapFailure(admission.FailureClassifierInvalidOutput, "route", errors.New("invalid json")),
			},
			input:    "Santa what do you think",
			wantCat:  admission.FailureClassifierInvalidOutput,
			wantText: "classifier invalid output",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notice, ok := BuildAdmissionRetryNotice(tc.err, tc.input)
			if !ok {
				t.Fatal("expected manual retry notice")
			}
			if notice.Category != tc.wantCat {
				t.Fatalf("category=%q want=%q", notice.Category, tc.wantCat)
			}
			if !strings.Contains(notice.Message, tc.wantText) {
				t.Fatalf("message=%q missing %q", notice.Message, tc.wantText)
			}
			if !strings.Contains(notice.Message, "Original input:") || !strings.Contains(notice.Message, tc.input) {
				t.Fatalf("message=%q missing original input", notice.Message)
			}
		})
	}
}

func TestBuildAdmissionRetryNoticeSkipsNonRetryableFailures(t *testing.T) {
	cancelled := &pipeline.AdmissionError{
		Err: admission.WrapFailure(admission.FailureParentCancellation, "route", errors.New("canceled")),
	}
	if notice, ok := BuildAdmissionRetryNotice(cancelled, "hello"); ok || notice.Message != "" {
		t.Fatalf("parent cancellation should not produce notice: %+v ok=%v", notice, ok)
	}
	if notice, ok := BuildAdmissionRetryNotice(errors.New("plain failure"), "hello"); ok || notice.Message != "" {
		t.Fatalf("plain failure should not produce notice: %+v ok=%v", notice, ok)
	}
}

func TestRunTextModeAdmissionFailureEmitsRetryNoticeAndKeepsOriginalInput(t *testing.T) {
	bus := events.NewBus()
	p := &pipeline.Pipeline{Events: bus, Brain: &appTestBrain{}}
	p.AdmitInput = func(context.Context, string, bool) (pipeline.ResponseAdmission, error) {
		return pipeline.ResponseAdmission{}, admission.WrapFailure(
			admission.FailureClassifierUnavailable,
			"route",
			errors.New("classifier offline"),
		)
	}

	var inputs []string
	var infos []string
	events.Subscribe(bus, func(e events.UserInput) { inputs = append(inputs, e.Text) })
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })

	if err := Run(context.Background(), p, strings.NewReader("Santa what do you think\nexit\n"), true, false); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0] != "Santa what do you think" {
		t.Fatalf("user inputs=%v", inputs)
	}
	found := false
	for _, message := range infos {
		if strings.Contains(message, "classifier unavailable") && strings.Contains(message, "Original input: \"Santa what do you think\"") {
			found = true
		}
	}
	if !found {
		t.Fatalf("manual retry notice missing from info events: %v", infos)
	}
}
