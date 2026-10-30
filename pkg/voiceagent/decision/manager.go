package decision

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// Prepare constructs an explicitly chosen provider and, for managed local
// inference, its owned process. It must respect cancellation. No turn calls it.
type Prepare func(context.Context) (Evaluator, io.Closer, error)

type RuntimeStatus struct {
	Detail string
	Phase  string // off, preparing, ready, unavailable
	Err    error
}

type preparation struct {
	ctx    context.Context
	cancel context.CancelFunc
	closer io.Closer
}

// Manager keeps installation and cold startup outside the conversation path.
// A missing, loading or failed optional runtime leaves the controller off.
type Manager struct {
	mu         sync.Mutex
	controller *Controller
	current    *preparation
	status     RuntimeStatus
	closed     bool
	wg         sync.WaitGroup
}

func NewManager(controller *Controller) *Manager {
	return &Manager{controller: controller, status: RuntimeStatus{Phase: "off"}}
}

// Configure returns immediately after cancelling the previous generation.
// prepare=nil disables the feature without provisioning or credential lookup.
func (m *Manager) Configure(options Options, prepare Prepare) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.controller.Close()
	if m.current != nil {
		m.current.cancel()
		if closer := m.current.closer; closer != nil {
			m.wg.Add(1)
			go func() { defer m.wg.Done(); _ = closer.Close() }()
		}
	}
	m.current = nil
	m.status = RuntimeStatus{Phase: "off"}
	if prepare == nil || options.Budget <= 0 || (!options.VoiceWork && !options.TextWork) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &preparation{ctx: ctx, cancel: cancel}
	m.current = job
	m.status = RuntimeStatus{Phase: "preparing"}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		provider, closer, err := prepare(ctx)
		m.mu.Lock()
		if m.closed || m.current != job || ctx.Err() != nil {
			m.mu.Unlock()
			if closer != nil {
				_ = closer.Close()
			}
			return
		}
		if err != nil || provider == nil {
			m.status = RuntimeStatus{Phase: "unavailable", Err: err}
			job.cancel()
			m.mu.Unlock()
			if closer != nil {
				_ = closer.Close()
			}
			return
		}
		job.closer = closer
		options.Policy.Evaluator = provider
		m.controller.Configure(options)
		m.status = RuntimeStatus{Phase: "ready"}
		if info, ok := closer.(interface{ RuntimeInfo() string }); ok {
			m.status.Detail = info.RuntimeInfo()
		}
		if watched, ok := closer.(interface{ Done() <-chan struct{} }); ok {
			m.wg.Add(1)
			go func() {
				defer m.wg.Done()
				select {
				case <-ctx.Done():
					return
				case <-watched.Done():
				}
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.current == job && !m.closed {
					m.controller.Close()
					m.status = RuntimeStatus{Phase: "unavailable", Err: fmt.Errorf("local decision runtime stopped")}
				}
			}()
		}
		m.mu.Unlock()
	}()
}

func (m *Manager) Status() RuntimeStatus { m.mu.Lock(); defer m.mu.Unlock(); return m.status }

func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	m.controller.Close()
	var closer io.Closer
	if m.current != nil {
		m.current.cancel()
		closer = m.current.closer
		m.current = nil
	}
	m.status = RuntimeStatus{Phase: "off"}
	m.mu.Unlock()
	var err error
	if closer != nil {
		err = closer.Close()
	}
	m.wg.Wait()
	return err
}
