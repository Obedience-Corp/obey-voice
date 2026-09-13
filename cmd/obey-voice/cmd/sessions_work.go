package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/workjournal"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/spf13/cobra"
)

type sessionsWorkView struct {
	Schema              string             `json:"schema"`
	SessionID           string             `json:"session_id"`
	Source              string             `json:"source"`
	Sidecar             string             `json:"sidecar"`
	PersistedOnly       bool               `json:"persisted_only"`
	Projection          string             `json:"projection"`
	CapturedAt          time.Time          `json:"captured_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
	Age                 *float64           `json:"age_seconds"`
	CanonicalWatermark  uint64             `json:"canonical_watermark"`
	SelectedPersonaID   string             `json:"selected_persona_id"`
	SelectedPersonaName string             `json:"selected_persona_name"`
	Work                *work.Snapshot     `json:"work"`
	Diagnostics         workjournal.Report `json:"diagnostics"`
	Details             map[string]string  `json:"details,omitempty"`
	Incomplete          bool               `json:"incomplete"`
	Notes               []string           `json:"notes,omitempty"`
}

func newSessionsInspectCmd() *cobra.Command {
	c := &cobra.Command{Use: "inspect <id>", Short: "Inspect saved work and diagnostic health", Args: cobra.ExactArgs(1), RunE: runSessionsInspect}
	c.Flags().Bool("work", true, "Inspect saved background work")
	c.Flags().Bool("json", false, "Output a persisted-only JSON report")
	return c
}
func newSessionsExportCmd() *cobra.Command {
	c := &cobra.Command{Use: "export <id>", Short: "Export redacted work diagnostics to a local file", Args: cobra.ExactArgs(1), RunE: runSessionsExport}
	c.Flags().Bool("diagnostics", true, "Export work diagnostics")
	c.Flags().String("output", "", "Local output file (required)")
	c.Flags().Bool("include-details", false, "Include referenced redacted tool previews")
	c.Flags().Bool("overwrite", false, "Replace an unrelated existing output file")
	c.Flags().Bool("json", false, "Output export path and completeness as JSON")
	return c
}

func loadSessionsWork(id string) (sessionsWorkView, *workjournal.Normalizer, error) {
	var v sessionsWorkView
	store := session.DefaultStore()
	dir, err := store.WorkDir(id)
	if err != nil {
		return v, nil, usageError("%v", err)
	}
	cfg, err := config.LoadRaw()
	if err != nil {
		return v, nil, err
	}
	n := workjournal.NewNormalizer(cfg)
	sess, err := store.Load(id)
	if err != nil {
		return v, n, classifySessionReadError(err)
	}
	now := time.Now().UTC()
	source := filepath.Join(config.SessionsDir(), id+".json")
	source, _ = filepath.Abs(source)
	dir, _ = filepath.Abs(dir)
	v = sessionsWorkView{Schema: "obey-voice.work-inspection/v1", SessionID: id, Source: source, Sidecar: dir, PersistedOnly: true, Projection: "bounded_redacted", CapturedAt: now, UpdatedAt: sess.UpdatedAt, SelectedPersonaID: n.Text(sess.SelectedPersonaID, 1024), SelectedPersonaName: n.Text(sess.SelectedPersonaDisplayName, 1024)}
	if !sess.UpdatedAt.IsZero() {
		age := max(0, now.Sub(sess.UpdatedAt).Seconds())
		v.Age = &age
	}
	if sess.Work != nil {
		safe := n.Snapshot(*sess.Work)
		v.Work = &safe
		v.CanonicalWatermark = sess.Work.PersistedRevision
	} else {
		v.Notes = append(v.Notes, "Saved work state is unavailable for this session")
	}
	report, readErr := workjournal.Read(dir)
	v.Diagnostics = n.Report(report)
	v.Incomplete = report.Incomplete || sess.Work == nil
	v.Notes = append(v.Notes, v.Diagnostics.Notes...)
	if readErr != nil {
		v.Incomplete = true
		if errors.Is(readErr, workjournal.ErrUnsupportedSchema) {
			return v, n, &ExitCodeError{Code: 4, Err: readErr}
		}
		if len(report.Records) == 0 && len(report.Checkpoints) == 0 {
			return v, n, &ExitCodeError{Code: 1, Err: readErr}
		}
		return v, n, &ExitCodeError{Code: 5, Err: fmt.Errorf("diagnostic history incomplete: %w", readErr)}
	}
	if v.Incomplete {
		return v, n, &ExitCodeError{Code: 5, Err: errors.New("diagnostic history incomplete")}
	}
	return v, n, nil
}

func classifySessionReadError(err error) error {
	code := 1
	if errors.Is(err, session.ErrUnsupportedSessionVersion) || errors.Is(err, workjournal.ErrUnsupportedSchema) {
		code = 4
	} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, session.ErrSessionNotFound) {
		code = 3
	}
	return &ExitCodeError{Code: code, Err: err}
}
func sessionInspectionError(c *cobra.Command, err error) error {
	if asJSON, _ := c.Flags().GetBool("json"); asJSON {
		code := map[int]string{1: "read_failed", 2: "invalid_input", 3: "not_found", 4: "unsupported_schema", 5: "history_incomplete"}[ExitCode(err)]
		_ = encodeJSON(c, struct {
			Schema string `json:"schema"`
			Code   string `json:"code"`
			Error  string `json:"error"`
		}{"obey-voice.work-inspection/v1", code, err.Error()})
	}
	return err
}
func runSessionsInspect(c *cobra.Command, args []string) error {
	c.SilenceUsage = true
	if enabled, _ := c.Flags().GetBool("work"); !enabled {
		return usageError("inspect currently requires --work")
	}
	v, _, err := loadSessionsWork(args[0])
	if v.Schema == "" {
		return sessionInspectionError(c, err)
	}
	if asJSON, _ := c.Flags().GetBool("json"); asJSON {
		if e := encodeJSON(c, v); e != nil {
			return e
		}
		return err
	}
	out := c.OutOrStdout()
	fmt.Fprintf(out, "Session %s (persisted only)\nSource: %s\nDiagnostics: %s\n", v.SessionID, v.Source, v.Sidecar)
	if v.Age != nil {
		fmt.Fprintf(out, "Saved %.1f seconds ago; canonical watermark %d\n", *v.Age, v.CanonicalWatermark)
	} else {
		fmt.Fprintln(out, "Saved age unknown")
	}
	fmt.Fprintf(out, "Selected responder: %s (%s)\n", v.SelectedPersonaName, v.SelectedPersonaID)
	if v.Work != nil {
		for _, j := range v.Work.Jobs {
			fmt.Fprintf(out, "Job %s: %s · %s · origin %s · %s/%s\n", j.Key.JobID, j.State, j.Task, j.Origin, j.Provider, j.Model)
		}
		for _, rows := range [][]work.ResultView{v.Work.Pending, v.Work.Communicated} {
			for _, r := range rows {
				fmt.Fprintf(out, "Result %s [%s, originating persona %s]: %s\n", r.Outcome.ResultID, r.State, r.Outcome.Config["persona"], r.Outcome.Text)
			}
		}
	}
	for _, note := range v.Notes {
		fmt.Fprintf(out, "Note: %s\n", note)
	}
	return err
}

func runSessionsExport(c *cobra.Command, args []string) error {
	c.SilenceUsage = true
	if enabled, _ := c.Flags().GetBool("diagnostics"); !enabled {
		return usageError("export currently requires --diagnostics")
	}
	output, _ := c.Flags().GetString("output")
	if strings.TrimSpace(output) == "" {
		return usageError("--output is required")
	}
	v, n, readErr := loadSessionsWork(args[0])
	if v.Schema == "" {
		return sessionInspectionError(c, readErr)
	}
	if err := protectInspectionSources(output, v); err != nil {
		return err
	}
	if include, _ := c.Flags().GetBool("include-details"); include {
		v.Details = map[string]string{}
		var used int
		for _, r := range v.Diagnostics.Records {
			var payload struct {
				Details workjournal.Detail `json:"details"`
			}
			if json.Unmarshal(r.Data, &payload) != nil || payload.Details.Ref == "" {
				continue
			}
			ref := payload.Details.Ref
			if _, exists := v.Details[ref]; exists {
				continue
			}
			body, err := workjournal.ReadDetail(v.Sidecar, ref)
			if err != nil || used+len(body) > 16<<20 {
				v.Incomplete = true
				v.Notes = append(v.Notes, "Retained detail unavailable or expired: "+n.Text(ref, 128))
				continue
			}
			v.Details[ref] = n.Text(string(body), 64<<10)
			used += len(body)
		}
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	overwrite, _ := c.Flags().GetBool("overwrite")
	if err = writeInspectionExport(output, append(b, '\n'), overwrite); err != nil {
		return err
	}
	if asJSON, _ := c.Flags().GetBool("json"); asJSON {
		if err := encodeJSON(c, struct {
			Schema     string `json:"schema"`
			Output     string `json:"output"`
			Incomplete bool   `json:"incomplete"`
		}{v.Schema, output, v.Incomplete}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(c.OutOrStdout(), "Exported persisted work diagnostics to %s\n", output)
	}
	if readErr != nil {
		return readErr
	}
	if v.Incomplete {
		return &ExitCodeError{Code: 5, Err: errors.New("diagnostic export incomplete")}
	}
	return nil
}

func protectInspectionSources(output string, v sessionsWorkView) error {
	abs, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return err
	}
	resolved := filepath.Join(parent, filepath.Base(abs))
	canonical, _ := filepath.EvalSymlinks(v.Source)
	sidecar, err := filepath.EvalSymlinks(v.Sidecar)
	if err != nil {
		sidecar = v.Sidecar
	}
	rel, _ := filepath.Rel(sidecar, resolved)
	if resolved == canonical || rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
		return usageError("export output must be outside the source session and its diagnostics")
	}
	if source, e := os.Stat(v.Source); e == nil {
		if dest, e := os.Stat(output); e == nil && os.SameFile(source, dest) {
			return usageError("export output is the canonical source session")
		}
	}
	return nil
}

func writeInspectionExport(output string, b []byte, overwrite bool) error {
	f, err := os.CreateTemp(filepath.Dir(output), ".obey-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if overwrite {
		err = os.Rename(f.Name(), output)
	} else {
		err = os.Link(f.Name(), output)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(output))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
