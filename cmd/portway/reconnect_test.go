package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/config"
	"github.com/radityama/portway/internal/relay"
	"github.com/radityama/portway/internal/transport"
)

// cuttableProxy forwards opaque TLS bytes and lets a test drop only the active
// transport while keeping the relay/public listener and CLI process alive.
func cuttableProxy(t *testing.T, upstream string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	var workers sync.WaitGroup
	acceptedDone := make(chan struct{})
	cut := func() {
		mu.Lock()
		defer mu.Unlock()
		for conn := range connections {
			conn.Close()
		}
	}
	go func() {
		defer close(acceptedDone)
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			outgoing, err := net.DialTimeout("tcp", upstream, time.Second)
			if err != nil {
				incoming.Close()
				continue
			}
			mu.Lock()
			connections[incoming] = struct{}{}
			connections[outgoing] = struct{}{}
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { mu.Lock(); delete(connections, incoming); delete(connections, outgoing); mu.Unlock() }()
				copied := make(chan struct{})
				go func() { io.Copy(outgoing, incoming); incoming.Close(); outgoing.Close(); close(copied) }()
				io.Copy(incoming, outgoing)
				incoming.Close()
				outgoing.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-acceptedDone; cut(); workers.Wait() })
	return listener.Addr().String(), cut
}

type eventWriter struct {
	bytes.Buffer // Read only after the CLI has joined.
	events       chan Event
}

func (w *eventWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	var event Event
	if json.Unmarshal(data, &event) == nil {
		w.events <- event
	}
	return n, err
}
func nextEvent(t *testing.T, events <-chan Event, name string) Event {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Event == "error" {
				t.Fatalf("unexpected terminal CLI event: %s", event.Error)
			}
			if event.Event == name {
				return event
			}
		case <-timer.C:
			t.Fatalf("CLI did not emit %s", name)
		}
	}
}

func publicCLIClient(t *testing.T, env config.Lookup) *http.Client {
	t.Helper()
	ca, _ := env("PORTWAY_RELAY_CA_FILE")
	trust, err := transport.ClientConfig(ca, "")
	if err != nil {
		t.Fatal(err)
	}
	remote := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trust.RootCAs, MinVersion: tls.VersionTLS13}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(address)
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	t.Cleanup(remote.CloseIdleConnections)
	return &http.Client{Transport: remote, Timeout: 3 * time.Second}
}

func TestCLIReconnectDuringActiveHTTPDoesNotReplay(t *testing.T) {
	env, token, relay := cliRelayFixture(t)
	upstream, _ := env("PORTWAY_RELAY_ADDR")
	proxy, cut := cuttableProxy(t, upstream)
	lookup := func(key string) (string, bool) {
		if key == "PORTWAY_RELAY_ADDR" {
			return proxy, true
		}
		if key == "PORTWAY_GENERATION" {
			return "10", true
		}
		return env(key)
	}
	var mutations atomic.Int32
	started := make(chan struct{})
	localCancelled := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mutate" {
			io.Copy(io.Discard, r.Body)
			mutations.Add(1)
			close(started)
			<-r.Context().Done()
			close(localCancelled)
			return
		}
		io.WriteString(w, "recovered")
	}))
	defer local.Close()
	_, port, _ := net.SplitHostPort(local.Listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	stdout := &eventWriter{events: make(chan Event, 64)}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{port}, lookup, stdout, &stderr) }()
	defer cancel()
	first := nextEvent(t, stdout.events, "tunnel_registered")
	ready := nextEvent(t, stdout.events, "ready")
	client := publicCLIClient(t, env)
	requestDone := make(chan int, 1)
	var interruptedConnectionClosed atomic.Bool
	go func() {
		response, err := client.Post(ready.PublicURL+"/mutate", "text/plain", strings.NewReader("one mutation"))
		if err != nil {
			interruptedConnectionClosed.Store(true)
			requestDone <- 0
			return
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		interruptedConnectionClosed.Store(response.Close)
		requestDone <- response.StatusCode
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("active request never reached local service")
	}
	cut()
	scheduled := nextEvent(t, stdout.events, "reconnect_scheduled")
	if scheduled.Attempt != 1 || scheduled.DelayMS < 500 || scheduled.DelayMS > 1000 || scheduled.Reason != "transport_closed" {
		t.Fatal("retry event lost bounded delay/reason")
	}
	select {
	case status := <-requestDone:
		if status == 200 {
			t.Fatal("interrupted request reported success")
		}
		if !interruptedConnectionClosed.Load() {
			t.Fatal("interrupted request left a cancelled HTTP connection reusable")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interrupted public request retained")
	}
	select {
	case <-localCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("old session retained local TCP work")
	}
	second := nextEvent(t, stdout.events, "tunnel_registered")
	readyAgain := nextEvent(t, stdout.events, "ready")
	if first.Generation != "10" || second.Generation != "11" || first.ConnectionID == second.ConnectionID || readyAgain.PublicURL != ready.PublicURL {
		t.Fatal("reconnect reused identity/generation or changed URL")
	}
	owner, exists := relay.Lookup(second.PublicHostname)
	if !exists || owner.ConnectionID != second.ConnectionID || owner.Generation.String() != "11" {
		t.Fatal("stale cleanup removed the new owner")
	}
	response, err := client.Get(readyAgain.PublicURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "recovered" || mutations.Load() != 1 {
		t.Fatalf("fresh traffic failed or mutation replayed: status=%d, bytes=%d, read_error=%v, mutations=%d", response.StatusCode, len(body), err, mutations.Load())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal("CLI recovery did not stop cleanly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect workers retained")
	}
	if strings.Contains(stdout.String(), token) || stderr.Len() != 0 {
		t.Fatal("reconnect JSON leaked token/human logs")
	}
}

func TestCLICancelsInitialConnectionBackoff(t *testing.T) {
	env, _, _ := cliRelayFixture(t)
	unavailable, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := unavailable.Addr().String()
	unavailable.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	port := strconv.Itoa(local.Listener.Addr().(*net.TCPAddr).Port)
	lookup := func(key string) (string, bool) {
		if key == "PORTWAY_RELAY_ADDR" {
			return address, true
		}
		return env(key)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &eventWriter{events: make(chan Event, 32)}
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{port}, lookup, stdout, io.Discard) }()
	nextEvent(t, stdout.events, "reconnect_scheduled")
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal("backoff cancellation failed")
		}
	case <-time.After(time.Second):
		t.Fatal("CLI backoff ignored cancellation")
	}
	if !strings.Contains(stdout.String(), `"event":"shutdown_complete"`) || strings.Contains(stdout.String(), `"event":"error"`) {
		t.Fatal("cancelled backoff emitted terminal error")
	}
}

func TestCLITerminalTLSAndRegistrationFailuresDoNotRetry(t *testing.T) {
	env, token, _ := cliRelayFixture(t)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	port := strconv.Itoa(local.Listener.Addr().(*net.TCPAddr).Port)
	fakeTokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(fakeTokenFile, []byte("invalid_credential_only_for_terminal_tests"), 0600); err != nil {
		t.Fatal(err)
	}
	// Keep a higher relay watermark but give the stale invocation a fresh local
	// counter. It must stop rather than repeatedly incrementing to seize ownership.
	if code := run(context.Background(), []string{"register", "--once"}, env, io.Discard, io.Discard); code != 0 {
		t.Fatal("could not reserve relay watermark")
	}
	staleState := t.TempDir()
	for _, mode := range []string{"tls", "scope", "authentication", "stale"} {
		lookup := func(key string) (string, bool) {
			if mode == "tls" && key == "PORTWAY_RELAY_SERVER_NAME" {
				return "wrong.invalid", true
			}
			if mode == "scope" && key == "PORTWAY_TUNNEL_ID" {
				return "tnl_wrong", true
			}
			if mode == "authentication" && key == "PORTWAY_TOKEN_FILE" {
				return fakeTokenFile, true
			}
			if mode == "stale" && key == "PORTWAY_STATE_DIR" {
				return staleState, true
			}
			return env(key)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var stdout, stderr bytes.Buffer
		code := run(ctx, []string{port}, lookup, &stdout, &stderr)
		cancel()
		if code != 1 || !strings.Contains(stdout.String(), `"event":"error"`) || strings.Contains(stdout.String(), "reconnect_scheduled") || strings.Contains(stdout.String(), token) || stderr.Len() != 0 {
			t.Fatalf("terminal %s failure was retried, hidden or unsafe (code %d)", mode, code)
		}
	}
}

type expiringVerifier struct{ auth.Verifier }

func (v expiringVerifier) Verify(ctx context.Context, token string) (auth.Identity, error) {
	identity, err := v.Verifier.Verify(ctx, token)
	identity.ExpiresAt = time.Now().Add(150 * time.Millisecond)
	return identity, err
}

func TestCLICredentialExpiryIsTerminal(t *testing.T) {
	env, _, _ := cliRelayFixture(t, func(server *relay.Server) { server.Authenticator = expiringVerifier{server.Authenticator} })
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	port := strconv.Itoa(local.Listener.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{port}, env, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "AUTH_EXPIRED") || strings.Contains(stdout.String(), "reconnect_scheduled") {
		t.Fatal("expired session entered a reconnect loop or hid its terminal reason")
	}
}

type registrationBarrier struct {
	entered chan struct{}
	release <-chan struct{}
}

func (h registrationBarrier) Enabled(context.Context, slog.Level) bool { return true }
func (h registrationBarrier) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "relay_registered" {
		h.entered <- struct{}{}
		select {
		case <-h.release:
		case <-ctx.Done():
		}
	}
	return nil
}
func (h registrationBarrier) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h registrationBarrier) WithGroup(string) slog.Handler      { return h }

func TestCLIReadyRequestWaitsForRelayRegistrationPublication(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	env, _, _ := cliRelayFixture(t, func(server *relay.Server) {
		server.Logger = slog.New(registrationBarrier{entered: entered, release: release})
	})
	t.Cleanup(unblock)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ready") }))
	defer local.Close()
	port := strconv.Itoa(local.Listener.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &eventWriter{events: make(chan Event, 32)}
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{port}, env, stdout, io.Discard) }()
	ready := nextEvent(t, stdout.events, "ready")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("relay did not reach registration publication")
	}
	responseDone := make(chan int, 1)
	client := publicCLIClient(t, env)
	go func() {
		response, err := client.Get(ready.PublicURL + "/")
		if err != nil {
			responseDone <- 0
			return
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		responseDone <- response.StatusCode
	}()
	select {
	case status := <-responseDone:
		unblock()
		t.Fatalf("ready request returned %d before relay published registration", status)
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case status := <-responseDone:
		if status != 200 {
			t.Fatalf("published registration returned %d", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registration wait retained request")
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal("publication test CLI failed")
		}
	case <-time.After(time.Second):
		t.Fatal("publication CLI retained")
	}
}
