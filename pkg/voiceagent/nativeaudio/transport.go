package nativeaudio

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// BindScheduler applies to this client's chat, ASR and shared speech adapter.
// Call once during preparation before publishing the native binding.
func (c *Client) BindScheduler(scheduler *Scheduler) error {
	if scheduler == nil {
		return errors.New("native HTTP scheduler is required")
	}
	if _, ok := c.http.Transport.(*scheduledTransport); ok {
		return errors.New("native HTTP scheduler already bound")
	}
	c.http.Transport = &scheduledTransport{base: c.http.Transport, scheduler: scheduler}
	return nil
}

type scheduledTransport struct {
	base      http.RoundTripper
	scheduler *Scheduler
}

func (t *scheduledTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if token, ok := request.Context().Value(schedulerContextKey{}).(*schedulerToken); ok && token.scheduler == t.scheduler {
		if !token.active.Load() {
			return nil, errors.New("native request belongs to a retired producer")
		}
		return t.base.RoundTrip(request)
	}
	ctx, release, err := t.scheduler.acquire(request.Context())
	if err != nil {
		return nil, err
	}
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil {
		release()
		return nil, err
	}
	if response.Body == nil {
		release()
		return response, nil
	}
	response.Body = &scheduledBody{ReadCloser: response.Body, release: release}
	return response, nil
}
func (t *scheduledTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type scheduledBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *scheduledBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

// selectedTransport binds SDK and speech-adapter use of this client to its
// approved endpoint. A mismatched candidate cannot send audio to another host.
type selectedTransport struct {
	base                 *http.Transport
	scheme, host, prefix string
}

func (t *selectedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != t.scheme || request.URL.Host != t.host || request.URL.User != nil || request.Header.Get("Authorization") != "" {
		return nil, errors.New("native request differs from selected endpoint or authentication")
	}
	if t.prefix != "" && request.URL.Path != t.prefix && !strings.HasPrefix(request.URL.Path, t.prefix+"/") {
		return nil, errors.New("native request escaped selected endpoint prefix")
	}
	return t.base.RoundTrip(request)
}
func (t *selectedTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
