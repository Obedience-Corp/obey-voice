package cmd

import (
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/workjournal"
)

func TestSessionsEventsCommandSurface(t *testing.T) {
	c := newSessionsEventsCmd()
	for _, name := range []string{"job", "follow", "json", "detail"} {
		if c.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s", name)
		}
	}
	if err := c.Args(c, []string{"a", "b"}); err == nil {
		t.Fatal("accepted more than one session id")
	}
}

func TestSessionsEventsClassifiesUnsupportedSchema(t *testing.T) {
	err := classifyEventsError(errors.Join(workjournal.ErrUnsupportedSchema, errors.New("future")))
	if ExitCode(err) != eventsUnsupported {
		t.Fatalf("exit code=%d, want %d", ExitCode(err), eventsUnsupported)
	}
}
