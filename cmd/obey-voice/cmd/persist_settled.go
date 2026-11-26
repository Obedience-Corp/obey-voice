package cmd

import (
	"context"
	"fmt"
	"io"
	"time"
)

// persistSettleTimeout bounds how long saving a conversation waits for a
// request a stopped turn left on the live provider. It outlasts the
// pipeline's own wait, after which such a request is given up on. A variable
// so tests can shorten it.
var persistSettleTimeout = 10 * time.Second

type sharedHistoryAwaiter interface {
	AwaitSharedHistory(context.Context) error
}

// saveSettled saves the conversation once requests stopped turns left on the
// live provider have ended: such a request writes the shared history until it
// does. One still ending after persistSettleTimeout skips this save with a
// warning rather than reading history it is writing.
func saveSettled(p sharedHistoryAwaiter, sessionID string, save func() error, warn io.Writer) {
	// The conversation's own context is already cancelled on quit.
	ctx, cancel := context.WithTimeout(context.Background(), persistSettleTimeout)
	defer cancel()
	if err := p.AwaitSharedHistory(ctx); err != nil {
		fmt.Fprintf(warn, "  warning: session %s not saved now, a stopped request is still ending: %v\n", sessionID, err)
		return
	}
	if err := save(); err != nil {
		fmt.Fprintf(warn, "  warning: failed to save session %s: %v\n", sessionID, err)
	}
}
