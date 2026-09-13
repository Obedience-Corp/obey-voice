package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/workjournal"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

const (
	eventsUnsupported = 4
	eventsIncomplete  = 5
)

type sessionsEventsSource struct {
	Schema        string `json:"schema"`
	Type          string `json:"type"`
	Source        string `json:"source"`
	PersistedOnly bool   `json:"persisted_only"`
	At            string `json:"at"`
}

// newSessionsEventsCmd builds the persisted-only work journal viewer.
func newSessionsEventsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "events <id>",
		Short: "Show persisted background-work events",
		Args:  cobra.ExactArgs(1),
		RunE:  runSessionsEvents,
	}
	c.Flags().String("job", "", "Filter events to a job ID")
	c.Flags().Bool("follow", false, "Follow committed events until canceled")
	c.Flags().Bool("json", false, "Output one JSON object per line")
	c.Flags().String("detail", "", "Read one retained redacted detail reference")
	return c
}

func runSessionsEvents(c *cobra.Command, args []string) error {
	c.SilenceUsage = true
	id := args[0]
	dir, err := session.DefaultStore().WorkDir(id)
	if err != nil {
		return usageError("invalid session id: %v", err)
	}
	if _, err := session.DefaultStore().Load(id); err != nil {
		return classifySessionReadError(fmt.Errorf("session %q: %w", id, err))
	}
	cfg, err := config.LoadRaw()
	if err != nil {
		return &ExitCodeError{Code: exitOperationFailed, Err: fmt.Errorf("load config: %w", err)}
	}
	normalizer := workjournal.NewNormalizer(cfg)
	jsonOut, _ := c.Flags().GetBool("json")
	if ref, _ := c.Flags().GetString("detail"); ref != "" {
		b, err := workjournal.ReadDetail(dir, ref)
		if err != nil {
			return &ExitCodeError{Code: exitOperationFailed, Err: err}
		}
		text := normalizer.Text(string(b), 64<<10)
		if jsonOut {
			return encodeJSON(c, struct {
				Schema string `json:"schema"`
				Type   string `json:"type"`
				Source string `json:"source"`
				Ref    string `json:"ref"`
				Text   string `json:"text"`
			}{workjournal.Schema, "journal.detail", dir, ref, text})
		}
		_, err = fmt.Fprintln(c.OutOrStdout(), text)
		return err
	}
	job, _ := c.Flags().GetString("job")
	follow, _ := c.Flags().GetBool("follow")
	if jsonOut {
		if err := writeJSONLine(c, sessionsEventsSource{Schema: workjournal.Schema, Type: "journal.source", Source: dir, PersistedOnly: true, At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
	}
	emit := func(ev workjournal.FollowEvent) error {
		if ev.Notice != nil {
			notice := *ev.Notice
			notice.RuntimeID = normalizer.Text(notice.RuntimeID, 1024)
			notice.Reason = normalizer.Text(notice.Reason, 1024)
			ev.Notice = &notice
		}
		if ev.Record != nil {
			r := normalizer.Record(*ev.Record)
			ev.Record = &r
		}
		if jsonOut {
			b, err := workjournal.FollowJSON(ev)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(c.OutOrStdout(), string(b))
			return err
		}
		if ev.Notice != nil {
			_, err := fmt.Fprintf(c.OutOrStdout(), "%s: %s %d-%d (%s)\n", ev.Notice.Type, ev.Notice.RuntimeID, ev.Notice.From, ev.Notice.To, ev.Notice.Reason)
			return err
		}
		if ev.Record != nil {
			_, err := fmt.Fprintf(c.OutOrStdout(), "%s %s/%d %s %s\n", ev.Record.At.Format(time.RFC3339), ev.Record.RuntimeID, ev.Record.Seq, ev.Record.Type, string(ev.Record.Data))
			return err
		}
		return nil
	}
	if follow {
		err := workjournal.Follow(c.Context(), dir, workjournal.FollowOptions{JobID: job}, emit)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return classifyEventsError(err)
	}
	rep, readErr := workjournal.Read(dir)
	if readErr != nil {
		if len(rep.Records) == 0 && len(rep.Checkpoints) == 0 {
			return classifyEventsError(readErr)
		}
	}
	rep = normalizer.Report(rep)
	for _, note := range rep.Notes {
		if jsonOut {
			if err := writeJSONLine(c, map[string]string{"schema": workjournal.Schema, "type": "journal.note", "message": note}); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(c.OutOrStdout(), "journal note: %s\n", note)
		}
	}
	results := map[string]string{}
	for _, cp := range rep.Checkpoints {
		b, _ := json.Marshal(map[string]any{"snapshot": cp.Snapshot})
		workjournal.LearnJobResults(workjournal.Record{RuntimeID: cp.Snapshot.Scope.RuntimeEpoch, ConversationEpoch: cp.Snapshot.Scope.ConversationEpoch, Type: "work.snapshot", Data: b}, results)
	}
	for i := range rep.Records {
		r := rep.Records[i]
		workjournal.LearnJobResults(r, results)
		if len(results) > 1024 {
			for key := range results {
				delete(results, key)
				break
			}
		}
		if job != "" && !workjournal.MatchJobCorrelated(r, job, results) {
			continue
		}
		if err := emit(workjournal.FollowEvent{Record: &r}); err != nil {
			return &ExitCodeError{Code: exitOperationFailed, Err: err}
		}
	}
	if errors.Is(readErr, workjournal.ErrUnsupportedSchema) {
		return &ExitCodeError{Code: eventsUnsupported, Err: readErr}
	}
	if readErr != nil || rep.Incomplete {
		return &ExitCodeError{Code: eventsIncomplete, Err: errors.New("diagnostic history incomplete")}
	}
	return nil
}

func writeJSONLine(c *cobra.Command, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(c.OutOrStdout(), string(b))
	return err
}

func classifyEventsError(err error) error {
	if err == nil {
		return nil
	}
	code := eventsUnsupported
	if !errors.Is(err, workjournal.ErrUnsupportedSchema) {
		code = exitOperationFailed
	}
	return &ExitCodeError{Code: code, Err: err}
}
