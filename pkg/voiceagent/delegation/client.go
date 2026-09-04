// Package delegation consumes Obey's owner-scoped delegation CLI. It owns
// conversational delivery only; Obey retains worker execution and durable state.
package delegation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Snapshot is the bounded projection returned by Obey's CLI.
type Snapshot struct {
	ID        string `json:"delegation_id"`
	State     string `json:"state"`
	Text      string `json:"text,omitempty"`
	Error     string `json:"error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (s Snapshot) Terminal() bool {
	switch s.State {
	case "completed", "failed", "canceled", "rejected", "interrupted", "cleanup_failed":
		return true
	}
	return false
}

type Request struct{ Task, Summary, Key string }

// Client operations are scoped by construction, never by model arguments.
type Client interface {
	Probe(context.Context) error
	Start(context.Context, Request) (Snapshot, error)
	Await(context.Context, string) (Snapshot, error)
	Cancel(context.Context, string, string) (Snapshot, error)
}

// CLIConfig names an operator-selected existing Obey parent session and profile.
// Its daemon must expose schema version 1 of the delegation CLI contract.
type CLIConfig struct{ Binary, Socket, CampaignID, ParentSessionID, Profile string }

type CLI struct{ cfg CLIConfig }

func NewCLI(cfg CLIConfig) (*CLI, error) {
	if cfg.Binary == "" {
		cfg.Binary = "obey"
	}
	if strings.TrimSpace(cfg.CampaignID) == "" || strings.TrimSpace(cfg.ParentSessionID) == "" || strings.TrimSpace(cfg.Profile) == "" {
		return nil, errors.New("delegation requires campaign ID, parent session ID and managed-child profile")
	}
	return &CLI{cfg: cfg}, nil
}

func (c *CLI) Probe(ctx context.Context) error {
	var caps struct {
		Version int  `json:"schema_version"`
		Managed bool `json:"managed_child"`
		Start   bool `json:"start"`
		Await   bool `json:"await"`
		Cancel  bool `json:"cancel"`
	}
	if err := c.run(ctx, "capabilities", []string{"--profile", c.cfg.Profile}, &caps); err != nil {
		return err
	}
	if caps.Version != 1 || !caps.Managed || !caps.Start || !caps.Await || !caps.Cancel {
		return errors.New("obey does not expose the required managed delegation start, await and cancel capabilities")
	}
	return nil
}

func (c *CLI) Start(ctx context.Context, r Request) (Snapshot, error) {
	args := []string{"--profile", c.cfg.Profile, "--task", r.Task, "--idempotency-key", r.Key}
	if r.Summary != "" {
		args = append(args, "--summary", r.Summary)
	}
	return c.snapshot(ctx, "start", args)
}
func (c *CLI) Await(ctx context.Context, id string) (Snapshot, error) {
	return c.snapshot(ctx, "await", []string{"--delegation-id", id, "--timeout", "1s"})
}
func (c *CLI) Cancel(ctx context.Context, id, reason string) (Snapshot, error) {
	return c.snapshot(ctx, "cancel", []string{"--delegation-id", id, "--reason", reason})
}
func (c *CLI) snapshot(ctx context.Context, verb string, args []string) (Snapshot, error) {
	var s Snapshot
	err := c.run(ctx, verb, args, &s)
	if err == nil && (s.ID == "" || s.State == "") {
		err = errors.New("obey returned an incomplete delegation snapshot")
	}
	return s, err
}

const maxCLIOutput = 128 * 1024

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxCLIOutput - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func (c *CLI) run(ctx context.Context, verb string, extra []string, result any) error {
	// Never use a shell: task text, paths and IDs are literal argv elements.
	args := []string{"delegation", verb, "--campaign-id", c.cfg.CampaignID, "--parent-session-id", c.cfg.ParentSessionID, "--json"}
	if c.cfg.Socket != "" {
		args = append(args, "--socket", c.cfg.Socket)
	}
	args = append(args, extra...)
	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...)
	cmd.WaitDelay = time.Second
	var stdout, stderr boundedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("obey delegation %s: %w", verb, err)
	}
	if stdout.overflow {
		return errors.New("obey delegation response exceeded 128 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("decode Obey delegation %s: %w", verb, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("obey delegation response contained trailing data")
	}
	return nil
}
