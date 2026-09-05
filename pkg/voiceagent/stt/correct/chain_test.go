package correct

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type funcStage struct {
	name string
	fn   func(string) (string, error)
}

func (s funcStage) Name() string { return s.name }
func (s funcStage) Apply(_ context.Context, text string) (string, error) {
	return s.fn(text)
}

func TestChainCorrect(t *testing.T) {
	upper := funcStage{"upper", func(s string) (string, error) { return strings.ToUpper(s), nil }}
	bang := funcStage{"bang", func(s string) (string, error) { return s + "!", nil }}
	boom := errors.New("boom")
	fail := funcStage{"fail", func(string) (string, error) { return "", boom }}

	tests := []struct {
		name    string
		stages  []Stage
		ctx     func() context.Context
		want    string
		wantErr error
	}{
		{"stage error keeps prior text", []Stage{upper, fail, bang}, context.Background, "HI", boom},
		{"cancelled before first stage", []Stage{upper}, cancelled, "hi", context.Canceled},
		{"no stages", nil, context.Background, "hi", nil},
		{"nil stage skipped", []Stage{nil, upper}, context.Background, "HI", nil},
		{"stages run in order", []Stage{upper, bang}, context.Background, "HI!", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(tc.stages...).Correct(tc.ctx(), "hi")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChainErrorNamesStage(t *testing.T) {
	fail := funcStage{"fail", func(string) (string, error) { return "", errors.New("boom") }}
	_, err := New(fail).Correct(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "stage fail") {
		t.Fatalf("err = %v", err)
	}
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
