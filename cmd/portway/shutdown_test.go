package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/radityama/portway/internal/relay"
)

func awaitCLI(t *testing.T, done <-chan int) {
	t.Helper()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("shutdown exit=%d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CLI shutdown did not join")
	}
}

func TestCLIAndRelayDrainActiveHTTP(t *testing.T) {
	for _, side := range []string{"agent", "relay"} {
		t.Run(side, func(t *testing.T) {
			env, token, server := cliRelayFixture(t, func(s *relay.Server) { s.ShutdownTimeout = time.Second })
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			payload := bytes.Repeat([]byte("graceful-response"), 100000)
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Write(payload)
			}))
			defer func() { unblock(); local.Close() }()
			_, port, _ := net.SplitHostPort(local.Listener.Addr().String())
			lookup := func(key string) (string, bool) {
				if key == "PORTWAY_SHUTDOWN_TIMEOUT" {
					return "1s", true
				}
				return env(key)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stdout := &eventWriter{events: make(chan Event, 64)}
			var stderr bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- run(ctx, []string{port}, lookup, stdout, &stderr) }()
			ready := nextEvent(t, stdout.events, "ready")
			client := publicCLIClient(t, env)
			requestDone := make(chan error, 1)
			go func() {
				response, err := client.Get(ready.PublicURL)
				if err != nil {
					requestDone <- err
					return
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				if err == nil && (response.StatusCode != 200 || !bytes.Equal(data, payload)) {
					err = errors.New("draining changed HTTP response")
				}
				requestDone <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not start")
			}
			drainDone := make(chan error, 1)
			if side == "agent" {
				cancel()
				e := nextEvent(t, stdout.events, "shutdown_started")
				if e.Reason != "user_shutdown" || e.Relay == "" {
					t.Fatal("local shutdown event lost context")
				}
			} else {
				go func() { drainDone <- server.Shutdown(context.Background()) }()
				e := nextEvent(t, stdout.events, "tunnel_draining")
				if e.Reason != "peer_shutdown" {
					t.Fatal("peer shutdown event changed")
				}
			}
			response, err := client.Get(ready.PublicURL + "/new")
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 503 {
				t.Fatalf("new request during drain=%d", response.StatusCode)
			}
			select {
			case <-done:
				t.Fatal("CLI returned before active response")
			case <-time.After(20 * time.Millisecond):
			}
			unblock()
			select {
			case err = <-requestDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("active response lost during shutdown")
			}
			if side == "relay" {
				select {
				case err = <-drainDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("relay shutdown did not join")
				}
				e := nextEvent(t, stdout.events, "reconnect_scheduled")
				if e.Reason != "relay_draining" {
					t.Fatal("relay drain did not schedule recovery")
				}
				if server.ActiveConnections() != 0 {
					t.Fatal("relay retained drained connection")
				}
				cancel()
			}
			awaitCLI(t, done)
			if bytes.Contains(stdout.Bytes(), []byte(token)) {
				t.Fatal("credential in shutdown events")
			}
		})
	}
}

func TestCLIDrainDeadlineClosesLocalSocket(t *testing.T) {
	env, _, _ := cliRelayFixture(t)
	started := make(chan struct{})
	closed := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(closed) }))
	defer local.Close()
	_, port, _ := net.SplitHostPort(local.Listener.Addr().String())
	lookup := func(key string) (string, bool) {
		if key == "PORTWAY_SHUTDOWN_TIMEOUT" {
			return "80ms", true
		}
		return env(key)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &eventWriter{events: make(chan Event, 64)}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{port}, lookup, stdout, &stderr) }()
	ready := nextEvent(t, stdout.events, "ready")
	client := publicCLIClient(t, env)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := client.Get(ready.PublicURL)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("local request missing")
	}
	now := time.Now()
	cancel()
	awaitCLI(t, done)
	if time.Since(now) > time.Second {
		t.Fatal("forced shutdown exceeded deadline")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("forced shutdown retained local socket")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("forced shutdown retained public request")
	}
}

func TestDiagnosticPeerDrainExitsCleanly(t *testing.T) {
	env, _, server := cliRelayFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &eventWriter{events: make(chan Event, 64)}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"register"}, env, stdout, &stderr) }()
	nextEvent(t, stdout.events, "tunnel_registered")
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitCLI(t, done)
	if bytes.Contains(stdout.Bytes(), []byte(`"event":"reconnect_scheduled"`)) {
		t.Fatal("diagnostic reconnect introduced")
	}
}

func TestRelayShutdownAbortsPendingTLSAndRejectsNewAdmissions(t *testing.T) {
	env, _, server := cliRelayFixture(t)
	address, _ := env("PORTWAY_RELAY_ADDR")
	stalled, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	deadline := time.Now().Add(time.Second)
	for server.ActiveConnections() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("stalled TLS not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	if err = server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	stalled.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = stalled.Read(make([]byte, 1)); err == nil {
		t.Fatal("pending TLS retained")
	}
	incoming, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Close()
	incoming.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = incoming.Read(make([]byte, 1)); err == nil {
		t.Fatal("draining relay admitted new socket")
	}
	if server.ActiveConnections() != 0 {
		t.Fatal("shutdown retained pending sockets")
	}
}

func TestDiagnosticDrainWaitsForAckPublication(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	env, _, server := cliRelayFixture(t, func(s *relay.Server) { s.Logger = slog.New(registrationBarrier{entered: entered, release: release}) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &eventWriter{events: make(chan Event, 64)}
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"register"}, env, stdout, io.Discard) }()
	nextEvent(t, stdout.events, "tunnel_registered")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("ACK publication barrier missing")
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- server.Shutdown(context.Background()) }()
	select {
	case <-done:
		t.Fatal("drain closed acknowledged session before publication")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ACK completion did not wake drain")
	}
	awaitCLI(t, done)
}
