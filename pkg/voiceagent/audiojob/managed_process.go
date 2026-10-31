package audiojob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// ManagedRuntime is owned by one finite worker. Its process inherits the
// worker's group so the editor can also reap it after a forced worker death.
type ManagedRuntime struct {
	endpoint, token, voice string
	client                 *http.Client
	cmd                    *exec.Cmd
	scratch                string
	done                   chan struct{}
	err                    error // published by closing done
	logs                   *managedLog
	once                   sync.Once
}

func StartManaged(ctx context.Context, c Config) (*ManagedRuntime, Provenance, error) {
	if err := c.Validate(); err != nil {
		return nil, Provenance{}, err
	}
	if c.Managed == nil {
		return nil, Provenance{}, fmt.Errorf("managed runtime config is required")
	}
	if err := c.Managed.verify(ctx); err != nil {
		return nil, Provenance{}, err
	}
	if err := safeDirectory(c.StorageRoot, c.StorageRoot+string(os.PathSeparator)+"runtime"); err != nil {
		return nil, Provenance{}, err
	}
	scratch, err := os.MkdirTemp(c.StorageRoot, ".audio-runtime-")
	if err != nil {
		return nil, Provenance{}, err
	}
	p := &ManagedRuntime{scratch: scratch, voice: c.Runtime.Voice, logs: &managedLog{}}
	cleanup := true
	defer func() {
		if cleanup {
			p.Close()
		}
	}()
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	command := exec.CommandContext(versionCtx, c.Managed.Executable.Path, "--version")
	command.Env, command.Dir = managedEnvironment(scratch), scratch
	command.WaitDelay = time.Second
	command.Stdout, command.Stderr = p.logs, p.logs
	err = command.Run()
	cancel()
	if err != nil {
		return nil, Provenance{}, fmt.Errorf("managed runtime version check failed: %w; %s", err, p.logs.String())
	}
	if !verifiedVersion(p.logs.String()) {
		return nil, Provenance{}, fmt.Errorf("managed runtime version is not %s; %s", ManagedVersion, p.logs.String())
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, Provenance{}, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return nil, Provenance{}, err
	}
	p.token = hex.EncodeToString(token[:])
	p.endpoint = "http://127.0.0.1:" + strconv.Itoa(port)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	p.client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	p.cmd = exec.CommandContext(ctx, c.Managed.Executable.Path,
		"--model", c.Managed.Model.Path, "--mmproj", c.Managed.Projector.Path,
		"--alias", ManagedModel, "--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--api-key", p.token, "--offline", "--no-webui", "--parallel", "1", "--ctx-size", "8192")
	p.cmd.Env, p.cmd.Dir = managedEnvironment(scratch), scratch
	p.cmd.Stdout, p.cmd.Stderr = p.logs, p.logs
	p.cmd.WaitDelay = time.Second
	if err = p.cmd.Start(); err != nil {
		return nil, Provenance{}, fmt.Errorf("start managed audio runtime: %w", err)
	}
	p.done = make(chan struct{})
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	if err = p.waitReady(ctx); err != nil {
		return nil, Provenance{}, err
	}
	cleanup = false
	return p, Provenance{RuntimeVersion: ManagedVersion, ModelDigest: c.Managed.Model.SHA256,
		ProjectorDigest: c.Managed.Projector.SHA256, RuntimeDigest: c.Managed.Executable.SHA256,
		Sampling: &Sampling{Seed: 42, MaxAudioFrames: 8192, RepeatPenalty: 1.05}}, nil
}

func (p *ManagedRuntime) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("managed audio readiness: %w", ctx.Err())
		case <-p.done:
			return fmt.Errorf("managed audio runtime exited: %v; %s", p.err, p.logs.String())
		case <-ticker.C:
			probeCtx, stop := context.WithTimeout(ctx, time.Second)
			ready, err := p.probe(probeCtx)
			stop()
			if err != nil {
				return err
			}
			if ready {
				return nil
			}
		}
	}
}

func (p *ManagedRuntime) probe(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/v1/models", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return false, nil // still loading; the owning deadline bounds this
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("owned audio runtime returned HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Data []struct {
			ID           string `json:"id"`
			Architecture struct {
				Output []string `json:"output_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc); err != nil {
		return false, fmt.Errorf("owned audio model metadata: %w", err)
	}
	if len(doc.Data) == 1 && doc.Data[0].ID == ManagedModel {
		for _, output := range doc.Data[0].Architecture.Output {
			if output == "audio" {
				return true, nil
			}
		}
	}
	return false, fmt.Errorf("owned runtime did not load the selected audio-output model")
}

func (p *ManagedRuntime) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		if p.done != nil {
			select {
			case <-p.done:
			default:
				_ = p.cmd.Process.Kill()
			}
			<-p.done
		}
		if p.client != nil {
			p.client.CloseIdleConnections()
		}
		_ = os.RemoveAll(p.scratch)
	})
}

type managedLog struct {
	mu   sync.Mutex
	text string
}

func (b *managedLog) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text += string(data)
	if len(b.text) > 8192 {
		b.text = b.text[len(b.text)-8192:]
	}
	return len(data), nil
}
func (b *managedLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.text }
