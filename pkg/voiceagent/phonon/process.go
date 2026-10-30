package phonon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Process struct {
	URL    string
	Device string
	cmd    *exec.Cmd
	done   chan struct{}
	err    error // published by closing done
	once   sync.Once
	logs   *tailBuffer
}

// Start uses an explicit backend and a private loopback port. offline is true
// for conversation startup; only explicit setup may download model assets.
func Start(ctx context.Context, o Options, offline bool) (*Process, error) {
	if err := Validate(o); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := l.Addr().String()
	_ = l.Close()
	cmd := exec.Command(o.binary(), "--addr", addr, "--lang", "en", "--device", o.Backend,
		"--model", "hf://"+Model+"@"+ModelRevision,
		"--voice-catalog", "hf://gradium/phonon-voices@"+CatalogRevision)
	configureCommand(cmd)
	vars := map[string]string{"HF_HOME": o.cache(), "HF_HUB_OFFLINE": "0", "RUST_LOG": "ptts_openai_server=info", "NO_COLOR": "1"}
	if offline {
		vars["HF_HUB_OFFLINE"] = "1"
	}
	cmd.Env = environment(vars)
	p := &Process{URL: "http://" + addr + "/v1", cmd: cmd, done: make(chan struct{}), logs: &tailBuffer{}}
	cmd.Stdout, cmd.Stderr = p.logs, p.logs
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start native Phonon: %w; run `obey-voice models ensure --tts`", err)
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	wait := 10 * time.Minute
	if offline {
		wait = 30 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	for {
		select {
		case <-readyCtx.Done():
			p.Close()
			return nil, fmt.Errorf("native Phonon readiness: %w; %s", readyCtx.Err(), p.logs.String())
		case <-p.done:
			return nil, fmt.Errorf("native Phonon (%s) exited: %v; %s", o.Backend, p.err, p.logs.String())
		case <-ticker.C:
			req, _ := http.NewRequestWithContext(readyCtx, http.MethodGet, p.URL+"/models", nil)
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			var doc struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc)
			resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				continue
			}
			for _, model := range doc.Data {
				if model.ID == Model {
					p.Device = deviceFromLog(p.logs.String())
					if p.Device == "" {
						p.Close()
						return nil, errors.New("native Phonon answered without its device banner; refusing to claim a verified runtime")
					}
					if !strings.HasPrefix(strings.ToLower(p.Device), o.Backend) {
						p.Close()
						return nil, fmt.Errorf("native Phonon requested %s but selected %s; refusing silent backend fallback", o.Backend, p.Device)
					}
					return p, nil
				}
			}
		}
	}
}

func deviceFromLog(s string) string {
	_, tail, ok := strings.Cut(s, "model loaded device=")
	if !ok {
		return ""
	}
	device, _, ok := strings.Cut(tail, " weights=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(device)
}

func (p *Process) checkVoices(ctx context.Context, required []string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+"/audio/voices", nil)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var doc struct {
		Voices []string `json:"voices"`
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("native Phonon voices returned %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return err
	}
	for _, want := range required {
		found := false
		for _, voice := range doc.Voices {
			if voice == want {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("native Phonon did not load required voice %s; %s", want, p.logs.String())
		}
	}
	return nil
}

func (p *Process) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = killCommand(p.cmd)
		<-p.done
	})
}

func (p *Process) Running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func environment(overrides map[string]string) []string {
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if _, ok := overrides[key]; ok || strings.HasPrefix(key, "PTTS_") {
			continue
		}
		env = append(env, v)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

type tailBuffer struct {
	mu   sync.Mutex
	text string
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text += string(p)
	if len(b.text) > 32768 {
		b.text = b.text[len(b.text)-32768:]
	}
	return len(p), nil
}
func (b *tailBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.text }
