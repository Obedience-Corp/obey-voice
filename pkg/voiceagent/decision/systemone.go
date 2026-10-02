package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type SystemOneConfig struct {
	// Provider must be explicitly local or jev. No implicit provider fallback.
	Provider string
	Endpoint string
	Model    string
	APIKey   string
	Client   *http.Client
}

type SystemOne struct {
	endpoint, model, key string
	client               *http.Client
}

// NewSystemOne configures transport only; it never starts a process, reads
// credentials from the environment, downloads weights, or makes a request.
func NewSystemOne(cfg SystemOneConfig) (*SystemOne, error) {
	if cfg.Endpoint == "" {
		switch cfg.Provider {
		case "local":
			cfg.Endpoint = "http://127.0.0.1:18763/v1/systemone"
		case "jev":
			cfg.Endpoint = "https://api.typesafe.ai/v1/systemone"
		}
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid decision endpoint")
	}
	switch cfg.Provider {
	case "local":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			host = "127.0.0.1"
			if port := u.Port(); port != "" {
				u.Host = net.JoinHostPort(host, port)
			} else {
				u.Host = host
			}
		}
		ip := net.ParseIP(host)
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || cfg.APIKey != "" {
			return nil, fmt.Errorf("local decisions require a loopback HTTP endpoint without credentials")
		}
		if cfg.Model == "" {
			cfg.Model = "decider"
		}
	case "jev":
		if u.Scheme != "https" || u.Host != "api.typesafe.ai" || strings.TrimSpace(cfg.APIKey) == "" || cfg.Model == "" {
			return nil, fmt.Errorf("jev decisions require the official HTTPS origin, a model and an API key")
		}
	default:
		return nil, fmt.Errorf("unknown system-one provider %q", cfg.Provider)
	}
	if u.Path != "/v1/systemone" {
		return nil, fmt.Errorf("decision endpoint must use /v1/systemone")
	}
	client := http.Client{}
	if cfg.Client != nil {
		client = *cfg.Client
	}
	if client.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.Provider == "local" {
			transport.Proxy = nil
		}
		client.Transport = transport
	} else if transport, ok := client.Transport.(*http.Transport); ok && cfg.Provider == "local" {
		transport = transport.Clone()
		transport.Proxy = nil
		client.Transport = transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &SystemOne{endpoint: u.String(), model: cfg.Model, key: cfg.APIKey, client: &client}, nil
}

func (s *SystemOne) Evaluate(ctx context.Context, input Request) (Response, error) {
	var result Response
	if err := ctx.Err(); err != nil {
		return result, err
	}
	body, err := json.Marshal(struct {
		Model string `json:"model"`
		Request
	}{s.model, input})
	if err != nil {
		return result, fmt.Errorf("encode decision: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("create decision request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.key != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return result, fmt.Errorf("decision request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("decision HTTP %d", response.StatusCode)
	}
	const maxBody = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return result, fmt.Errorf("read decision: %w", err)
	}
	if len(raw) > maxBody {
		return result, fmt.Errorf("decision response exceeds limit")
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("decode decision: %w", err)
	}
	result.Raw = raw
	return result, nil
}
