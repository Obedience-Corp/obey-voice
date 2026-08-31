package netapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// reserveLoopbackPort binds an ephemeral loopback port and releases it so a
// test can hand a concrete second address to ExtraBinds.
func reserveLoopbackPort(t *testing.T) int {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestListenAndServeExtraBindsServeSameHandler(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	dispatcher := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Run(ctx)

	extra := fmt.Sprintf("127.0.0.1:%d", reserveLoopbackPort(t))
	server := New(Options{
		Bind: "127.0.0.1:0",
		// The duplicate entry must be ignored, not become a bind conflict.
		ExtraBinds:  []string{extra, extra},
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  dispatcher,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	deadline := time.After(2 * time.Second)
	for len(server.Addrs()) < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected 2 bound addresses, have %v", server.Addrs())
		case <-time.After(5 * time.Millisecond):
		}
	}

	addrs := server.Addrs()
	if len(addrs) != 2 {
		t.Fatalf("Addrs() = %v, want exactly 2 (duplicate extra deduped)", addrs)
	}
	if addrs[0].String() != server.Addr().String() {
		t.Fatalf("Addr() %s must be the primary in Addrs() %v", server.Addr(), addrs)
	}

	client := insecureClient()
	for _, addr := range addrs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr.String()+"/v1/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+creds.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET status via %s: %v", addr, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET status via %s: code %d", addr, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestOnListeningReportsActualExtraAddresses(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	dispatcher := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Run(ctx)

	primary := fmt.Sprintf("127.0.0.1:%d", reserveLoopbackPort(t))
	got := make(chan []net.Addr, 1)
	server := New(Options{
		Bind: primary,
		// An ephemeral extra: the requested string is useless to clients —
		// the callback must carry the real port the listener received.
		ExtraBinds:  []string{"127.0.0.1:0"},
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  dispatcher,
		OnListening: func(addrs []net.Addr) { got <- addrs },
	})
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	var addrs []net.Addr
	select {
	case addrs = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("OnListening never fired")
	}
	if len(addrs) != 2 {
		t.Fatalf("OnListening addrs = %v, want primary + extra", addrs)
	}
	if addrs[0].String() != primary {
		t.Fatalf("primary must come first: %v (want %s)", addrs, primary)
	}
	extra, ok := addrs[1].(*net.TCPAddr)
	if !ok || extra.Port == 0 {
		t.Fatalf("extra must carry its actual ephemeral port, got %v", addrs[1])
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestListenAndServeRejectsPublicExtraBind(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := New(Options{
		Bind:        "127.0.0.1:0",
		ExtraBinds:  []string{"8.8.8.8:443"},
		Credentials: creds,
		Bus:         events.NewBus(),
		Dispatcher:  NewDispatcher(&scriptedRunner{}, events.NewBus(), nil, nil),
	})
	err = server.ListenAndServe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to bind") {
		t.Fatalf("public extra bind must be refused, got %v", err)
	}
}

func TestLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr net.Addr
		want bool
	}{
		{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7262}, true},
		{&net.TCPAddr{IP: net.IPv6loopback, Port: 7262}, true},
		{&net.TCPAddr{IP: net.IPv4(192, 168, 0, 188), Port: 7262}, false},
		{&net.TCPAddr{IP: net.IPv4(100, 64, 0, 7), Port: 7262}, false},
		{&net.TCPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 7262}, false},
		{nil, false},
	}
	for _, tc := range cases {
		name := "nil"
		if tc.addr != nil {
			name = tc.addr.String()
		}
		t.Run(name, func(t *testing.T) {
			if got := loopbackAddr(tc.addr); got != tc.want {
				t.Fatalf("loopbackAddr(%v) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

func TestLoopbackHTTPServesPlainHTTP(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	dispatcher := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Run(ctx)

	server := New(Options{
		Bind:         "127.0.0.1:0",
		LoopbackHTTP: true,
		Credentials:  creds,
		Bus:          bus,
		Dispatcher:   dispatcher,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	deadline := time.After(2 * time.Second)
	for server.Addr() == nil {
		select {
		case <-deadline:
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
	addr := server.Addr().String()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP GET status: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP GET status: code %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	unauth, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	unauthResp, err := http.DefaultClient.Do(unauth)
	if err != nil {
		t.Fatalf("HTTP GET without token: %v", err)
	}
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HTTP without token = %d, want 401", unauthResp.StatusCode)
	}
	_ = unauthResp.Body.Close()

	httpsCtx, httpsCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer httpsCancel()
	httpsReq, err := http.NewRequestWithContext(httpsCtx, http.MethodGet, "https://"+addr+"/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insecureClient().Do(httpsReq); err == nil {
		t.Fatal("HTTPS on LoopbackHTTP loopback unexpectedly succeeded")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestLoopbackHTTPLeavesNonLoopbackOnTLS(t *testing.T) {
	// Mixed bind: unspecified primary is not loopback so it stays ServeTLS;
	// a loopback extra is Serve. 0.0.0.0 is used because a real LAN IP is
	// not guaranteed in CI.
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	dispatcher := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Run(ctx)

	got := make(chan []net.Addr, 1)
	server := New(Options{
		Bind:         "0.0.0.0:0",
		AllowPublic:  true,
		ExtraBinds:   []string{"127.0.0.1:0"},
		LoopbackHTTP: true,
		Credentials:  creds,
		Bus:          bus,
		Dispatcher:   dispatcher,
		OnListening:  func(addrs []net.Addr) { got <- addrs },
	})
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	var addrs []net.Addr
	select {
	case addrs = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("OnListening never fired")
	}
	if len(addrs) != 2 {
		t.Fatalf("Addrs() = %v, want primary + extra", addrs)
	}
	_, primaryPort, err := net.SplitHostPort(addrs[0].String())
	if err != nil {
		t.Fatal(err)
	}
	tlsAddr := net.JoinHostPort("127.0.0.1", primaryPort)
	httpAddr := addrs[1].String()
	if !loopbackAddr(addrs[1]) {
		t.Fatalf("extra %s should be loopback", httpAddr)
	}

	status := func(rawURL string, client *http.Client) (*http.Response, error) {
		t.Helper()
		reqCtx, reqCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer reqCancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+creds.Token)
		return client.Do(req)
	}

	resp, err := status("http://"+httpAddr+"/v1/status", http.DefaultClient)
	if err != nil {
		t.Fatalf("HTTP on loopback extra: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP on loopback extra: code %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	httpsOnHTTP, err := status("https://"+httpAddr+"/v1/status", insecureClient())
	if err == nil {
		_ = httpsOnHTTP.Body.Close()
		t.Fatal("HTTPS on LoopbackHTTP extra unexpectedly succeeded")
	}

	resp, err = status("https://"+tlsAddr+"/v1/status", insecureClient())
	if err != nil {
		t.Fatalf("HTTPS on non-loopback primary: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS on non-loopback primary: code %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	httpOnTLS, err := status("http://"+tlsAddr+"/v1/status", http.DefaultClient)
	if err == nil {
		code := httpOnTLS.StatusCode
		_ = httpOnTLS.Body.Close()
		if code == http.StatusOK {
			t.Fatal("HTTP on non-loopback primary served /v1/status")
		}
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}
