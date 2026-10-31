package nativeaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

const SetupCheckTimeout = 15 * time.Second
const metadataLimit = 4 << 20

type Metadata struct {
	Endpoint       string   `json:"endpoint"`
	RuntimeVersion string   `json:"runtime_version"`
	Model          string   `json:"model"`
	Digest         string   `json:"digest"`
	Capabilities   []string `json:"capabilities"`
}

func (c *Client) Endpoint() string { return strings.TrimRight(c.base.String(), "/") }
func (c *Client) metadataRequest(parent context.Context, method, route string, payload any, result any) error {
	ctx, cancel := context.WithTimeout(parent, SetupCheckTimeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(route), body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("native setup %s: %w", route, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, metadataLimit+1))
	if err != nil {
		return err
	}
	if len(data) > metadataLimit {
		return errors.New("native setup metadata exceeds size limit")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("native setup %s HTTP %d", route, response.StatusCode)
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("native setup %s JSON: %w", route, err)
	}
	return nil
}
func (c *Client) Metadata(ctx context.Context, requireTools bool) (Metadata, error) {
	metadata := Metadata{Endpoint: c.Endpoint(), Model: c.model}
	var version struct {
		Version string `json:"version"`
	}
	if err := c.metadataRequest(ctx, http.MethodGet, "/api/version", nil, &version); err != nil {
		return Metadata{}, err
	}
	if version.Version == "" {
		return Metadata{}, errors.New("native runtime did not identify its version")
	}
	metadata.RuntimeVersion = version.Version
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if err := c.metadataRequest(ctx, http.MethodGet, "/api/tags", nil, &tags); err != nil {
		return Metadata{}, err
	}
	for _, model := range tags.Models {
		if model.Name == c.model || strings.TrimSuffix(model.Name, ":latest") == c.model {
			metadata.Digest = model.Digest
			break
		}
	}
	if len(metadata.Digest) != 64 || strings.IndexFunc(metadata.Digest, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) >= 0 {
		return Metadata{}, errors.New("selected native model is absent or has no valid digest")
	}
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := c.metadataRequest(ctx, http.MethodPost, "/api/show", map[string]string{"model": c.model}, &show); err != nil {
		return Metadata{}, err
	}
	needed := []string{"completion", "audio", "speech"}
	if requireTools {
		needed = append(needed, "tools")
	}
	for _, capability := range needed {
		if !slices.Contains(show.Capabilities, capability) {
			return Metadata{}, fmt.Errorf("selected native model lacks %s capability", capability)
		}
	}
	metadata.Capabilities = slices.Clone(show.Capabilities)
	slices.Sort(metadata.Capabilities)
	return metadata, nil
}
