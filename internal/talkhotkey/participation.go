package talkhotkey

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
)

// SetParticipationHandler extends the existing authenticated per-user endpoint.
func (c *Control) SetParticipationHandler(fn func(context.Context, *discussion.Settings) (discussion.Settings, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.participation = fn
}
func (c *Control) dispatchParticipation(conn net.Conn, payload string) error {
	c.mu.Lock()
	handler := c.participation
	c.mu.Unlock()
	if handler == nil {
		return writeReply(conn, "error: participation is unavailable")
	}
	var settings *discussion.Settings
	if err := json.Unmarshal([]byte(payload), &settings); err != nil {
		return writeReply(conn, "error: invalid participation request")
	}
	if settings != nil {
		if err := settings.Validate(); err != nil {
			return writeReply(conn, "error: "+err.Error())
		}
	}
	ctx, cancel := context.WithTimeout(c.requestCtx, controlIdleTimeout)
	defer cancel()
	result, err := handler(ctx, settings)
	if err != nil {
		return writeReply(conn, "error: "+strings.ReplaceAll(err.Error(), "\n", " "))
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return writeReply(conn, "error: cannot encode participation settings")
	}
	return writeReply(conn, "ok "+string(encoded))
}

// Participation queries (nil) or updates the running conversation; never writes global config.
func Participation(ctx context.Context, settings *discussion.Settings) (discussion.Settings, error) {
	if settings != nil {
		if err := settings.Validate(); err != nil {
			return discussion.Settings{}, err
		}
	}
	network, address, token, err := controlTarget()
	if err != nil {
		return discussion.Settings{}, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return discussion.Settings{}, fmt.Errorf("%w: %w", ErrNoListener, err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(controlIdleTimeout))
	reader := bufio.NewReader(io.LimitReader(conn, controlLineLimit*4))
	if token != "" {
		if err := exchange(conn, reader, "token "+token); err != nil {
			return discussion.Settings{}, err
		}
	}
	data, _ := json.Marshal(settings)
	if _, err := fmt.Fprintf(conn, "participation %s\n", data); err != nil {
		return discussion.Settings{}, err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return discussion.Settings{}, err
	}
	payload, ok := strings.CutPrefix(strings.TrimSpace(line), "ok ")
	if !ok {
		return discussion.Settings{}, fmt.Errorf("participation control: %s", strings.TrimSpace(line))
	}
	var result discussion.Settings
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return result, err
	}
	return result, result.Validate()
}
