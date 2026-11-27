// Package ollamamem estimates whether a local Ollama host has the memory to
// load a model beside the ones already loaded. It only reads the host's
// model lists and never asks it to load or run anything.
package ollamamem

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
)

// DefaultHost is where Ollama listens when no host is configured.
const DefaultHost = "http://localhost:11434"

// ProbeTimeout bounds each read of a host's model lists.
const ProbeTimeout = 2 * time.Second

// NormalizeHost spells a host so that the same server reads the same way:
// default scheme and port filled in, and any loopback name read as localhost.
// loopback is false for a remote host or one that does not parse.
func NormalizeHost(raw string) (host string, loopback bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultHost
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return strings.ToLower(raw), false
	}
	scheme := strings.ToLower(u.Scheme)
	name := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "11434"
		}
	}
	ip := net.ParseIP(name)
	loopback = name == "localhost" || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified()))
	if loopback {
		name = "localhost"
	}
	return scheme + "://" + net.JoinHostPort(name, port), loopback
}

// NormalizeModel spells a model name as Ollama lists it, without ":latest".
func NormalizeModel(model string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), ":latest")
}

// hostState is what one host reported at its last successful read.
type hostState struct {
	ok     bool
	loaded map[string]int64
	onDisk map[string]int64
	// peak is the largest loaded size seen for each model in this process.
	peak map[string]int64
}

// Monitor caches what local Ollama hosts and the system last reported.
// Decide reads only the cache; Refresh updates it.
type Monitor struct {
	// FreeMemory reads free system memory; nil uses the package's reader.
	FreeMemory func(context.Context) (uint64, bool)
	// HTTPClient reads the hosts; nil uses one bounded by ProbeTimeout that
	// refuses redirects.
	HTTPClient *http.Client

	mu        sync.Mutex
	hosts     map[string]*hostState
	free      uint64
	freeKnown bool
}

// New returns a Monitor with nothing cached: every Decide admits until the
// first Refresh.
func New() *Monitor {
	return &Monitor{}
}

func (m *Monitor) client() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return &http.Client{Timeout: ProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirects are not permitted for local Ollama probes")
	}}
}

// Refresh reads each loopback host's loaded and installed models and the
// free system memory. A host that cannot be read is marked unknown, which
// admits its jobs. Remote hosts are skipped.
func (m *Monitor) Refresh(ctx context.Context, hosts ...string) {
	read := map[string]*hostState{}
	for _, raw := range hosts {
		host, loopback := NormalizeHost(raw)
		if !loopback {
			continue
		}
		if _, done := read[host]; done {
			continue
		}
		state, err := m.probe(ctx, host)
		if err != nil {
			state = &hostState{}
		}
		read[host] = state
	}
	freeMemory := m.FreeMemory
	if freeMemory == nil {
		freeMemory = FreeMemory
	}
	free, freeKnown := freeMemory(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hosts == nil {
		m.hosts = map[string]*hostState{}
	}
	for host, state := range read {
		m.storeLocked(host, state)
	}
	m.free, m.freeKnown = free, freeKnown
}

func (m *Monitor) storeLocked(host string, state *hostState) {
	if prev := m.hosts[host]; prev != nil {
		state.peak = prev.peak
	}
	if state.peak == nil {
		state.peak = map[string]int64{}
	}
	for name, size := range state.loaded {
		state.peak[name] = max(state.peak[name], size)
	}
	m.hosts[host] = state
}

func (m *Monitor) probe(ctx context.Context, host string) (*hostState, error) {
	base, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse ollama host: %w", err)
	}
	client := api.NewClient(base, m.client())
	running, err := client.ListRunning(ctx)
	if err != nil {
		return nil, fmt.Errorf("list loaded models: %w", err)
	}
	installed, err := client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list installed models: %w", err)
	}
	state := &hostState{ok: true, loaded: map[string]int64{}, onDisk: map[string]int64{}}
	for _, model := range running.Models {
		state.loaded[NormalizeModel(model.Name)] = model.Size
	}
	for _, model := range installed.Models {
		state.onDisk[NormalizeModel(model.Name)] = model.Size
	}
	return state, nil
}

// Decide reports whether a job on model may start on host now, from the
// cached readings. A loaded model starts. Otherwise its need is the largest
// size it was seen loaded at, else its size on disk, which is a lower bound:
// its context adds to it. It waits only when that need is known to exceed
// free memory; anything unknown admits it, and Ollama still arbitrates.
func (m *Monitor) Decide(host, model string) (ok bool, reason string) {
	host, loopback := NormalizeHost(host)
	if !loopback {
		return true, ""
	}
	name := NormalizeModel(model)
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.hosts[host]
	if state == nil || !state.ok || name == "" {
		return true, ""
	}
	if _, loaded := state.loaded[name]; loaded {
		return true, ""
	}
	need := state.peak[name]
	if need <= 0 {
		need = state.onDisk[name]
	}
	if need <= 0 || !m.freeKnown || uint64(need) <= m.free {
		return true, ""
	}
	return false, fmt.Sprintf("waiting for memory (needs about %s, %s free)", formatGB(uint64(need)), formatGB(m.free))
}

func formatGB(n uint64) string {
	gb := float64(n) / 1e9
	if gb < 10 {
		return fmt.Sprintf("%.1f GB", gb)
	}
	return fmt.Sprintf("%.0f GB", gb)
}
