package mux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

func drainPair(t *testing.T, accept func(*Stream, protocol.OpenStream), configure ...func(*Options)) (*Conn, *Conn) {
	t.Helper()
	a, b := net.Pipe()
	opts := Options{MaxStreams: 2, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: 3 * time.Second, WriteTimeout: time.Second, IdleTimeout: 3 * time.Second, ExpiresAt: time.Now().Add(time.Minute), GracefulShutdown: true, ShutdownTimeout: time.Second, Heartbeat: true, HeartbeatInterval: 10 * time.Millisecond, HeartbeatTimeout: time.Second}
	for _, apply := range configure {
		apply(&opts)
	}
	origin, err := New(context.Background(), a, a, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Accept = accept
	remote, err := New(context.Background(), b, b, opts)
	if err != nil {
		t.Fatal(err)
	}
	done := []chan struct{}{make(chan struct{}), make(chan struct{})}
	for i, c := range []*Conn{origin, remote} {
		go func() { defer close(done[i]); _ = c.Run() }()
	}
	t.Cleanup(func() {
		origin.Close()
		remote.Close()
		for _, d := range done {
			select {
			case <-d:
			case <-time.After(time.Second):
				t.Error("drain worker leaked")
			}
		}
	})
	return origin, remote
}

func waitDrain(t *testing.T, c *Conn) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !c.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("admission did not close")
		}
		time.Sleep(time.Millisecond)
	}
}

// Receiving FIN is not proof the application has consumed its queued DATA.
func TestShutdownPreservesBufferedResponse(t *testing.T) {
	payload := bytes.Repeat([]byte("queued"), 4000)
	finished := make(chan struct{})
	origin, remote := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		defer close(finished)
		if s.Accept() != nil {
			return
		}
		_, _ = io.Copy(io.Discard, s)
		_, _ = s.Write(payload)
		_ = s.CloseWrite()
		_ = s.Close()
	})
	stream, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("sender did not finish")
	}
	done := make(chan error, 1)
	go func() { done <- remote.Shutdown(context.Background()) }()
	waitDrain(t, origin)
	if _, err = origin.Open(context.Background(), request()); !errors.Is(err, ErrDraining) {
		t.Fatal("new OPEN admitted during drain")
	}
	select {
	case <-done:
		t.Fatal("socket closed with unread response")
	case <-time.After(20 * time.Millisecond):
	}
	data, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("draining changed response: bytes=%d err=%v", len(data), err)
	}
	stream.Close()
	select {
	case err = <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain did not finish")
	}
}

func TestShutdownKeepsDuplexCreditAndHeartbeat(t *testing.T) {
	origin, _ := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() != nil {
			return
		}
		_, _ = io.Copy(s, s)
		_ = s.CloseWrite()
		_ = s.Close()
	})
	stream, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- origin.Shutdown(context.Background()) }()
	waitDrain(t, origin)
	payload := bytes.Repeat([]byte("duplex"), 200000)
	wrote := make(chan error, 1)
	go func() {
		_, e := stream.Write(payload)
		if e == nil {
			e = stream.CloseWrite()
		}
		wrote <- e
	}()
	data, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("drain lost duplex bytes: %d err=%v", len(data), err)
	}
	if err = <-wrote; err != nil {
		t.Fatal(err)
	}
	stream.Close()
	select {
	case err = <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("duplex drain hung")
	}
}

func TestShutdownWaitsAcceptanceCleanup(t *testing.T) {
	cleanup := make(chan struct{})
	reached := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(cleanup) })
	origin, remote := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() != nil {
			return
		}
		<-s.Context().Done()
		close(reached)
		<-cleanup
	})
	stream, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	stream.Reset(protocol.StreamCancelled)
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("reset did not reach worker")
	}
	done := make(chan error, 1)
	go func() { done <- remote.Shutdown(context.Background()) }()
	waitDrain(t, remote)
	deadline := time.Now().Add(time.Second)
	for remote.ActiveStreams() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("reset stream retained")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("drain returned before callback cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	release.Do(func() { close(cleanup) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not wake drain")
	}
}

func TestShutdownDeadlineCancelsWorkers(t *testing.T) {
	worker := make(chan struct{})
	origin, _ := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		defer close(worker)
		if s.Accept() == nil {
			<-s.Context().Done()
		}
	})
	if _, err := origin.Open(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := origin.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
	select {
	case <-worker:
	case <-time.After(time.Second):
		t.Fatal("deadline did not cancel acceptance")
	}
	if origin.ActiveStreams() != 0 {
		t.Fatal("forced shutdown retained streams")
	}
}

func TestShutdownInterruptsBlockedGoAway(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	opts := Options{MaxStreams: 1, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: time.Now().Add(time.Minute), GracefulShutdown: true}
	c, err := New(context.Background(), a, a, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if c.Shutdown(ctx) == nil || time.Since(start) > 300*time.Millisecond {
		t.Fatal("blocked GOAWAY ignored deadline")
	}
}

func TestGoAwayStateViolations(t *testing.T) {
	for _, codes := range [][]string{{protocol.GoAwayDrained}, {protocol.GoAwayShutdown, protocol.GoAwayShutdown}, {protocol.GoAwayShutdown, protocol.GoAwayDrained, protocol.GoAwayDrained}} {
		a, b := net.Pipe()
		opts := Options{MaxStreams: 1, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: time.Now().Add(time.Minute), GracefulShutdown: true}
		c, err := New(context.Background(), a, a, opts)
		if err != nil {
			t.Fatal(err)
		}
		for i, code := range codes {
			f, _ := protocol.EncodeGoAway(protocol.GoAway{Code: code})
			err = c.receiveGoAway(f)
			if (i == len(codes)-1) != (err != nil) {
				t.Fatal("GOAWAY ordering accepted")
			}
		}
		c.Close()
		b.Close()
	}
}

func TestShutdownLegacyDoesNotSendGoAway(t *testing.T) {
	origin, _ := pair(t, func(s *Stream, _ protocol.OpenStream) { s.Reject(protocol.StreamLimit) })
	if err := origin.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownCannotExtendFirstDeadline(t *testing.T) {
	origin, _ := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() == nil {
			<-s.Context().Done()
		}
	})
	if _, err := origin.Open(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	first, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- origin.Shutdown(first) }()
	waitDrain(t, origin)
	origin.mu.Lock()
	deadline := origin.drainDeadline
	origin.mu.Unlock()
	start := time.Now()
	_ = origin.Shutdown(context.Background())
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("repeated drain extended deadline")
	}
	origin.mu.Lock()
	same := origin.drainDeadline.Equal(deadline)
	origin.mu.Unlock()
	if !same {
		t.Fatal("repeated drain moved deadline")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first drain stranded")
	}
}

func TestShutdownCredentialExpiryStillWins(t *testing.T) {
	origin, _ := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() == nil {
			<-s.Context().Done()
		}
	}, func(o *Options) { o.ExpiresAt = time.Now().Add(60 * time.Millisecond) })
	if _, err := origin.Open(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if origin.Shutdown(context.Background()) == nil {
		t.Fatal("expiry reported completed drain")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("draining extended credentials")
	}
}

func TestRunRejectsUnnegotiatedAndMalformedGoAway(t *testing.T) {
	for _, kind := range []string{"unnegotiated", "malformed", "premature complete"} {
		t.Run(kind, func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			o := Options{MaxStreams: 1, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: time.Now().Add(time.Minute), Diagnostic: true, GracefulShutdown: kind != "unnegotiated"}
			c, err := New(context.Background(), a, a, o)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			done := make(chan error, 1)
			go func() { done <- c.Run() }()
			f, _ := protocol.EncodeGoAway(protocol.GoAway{Code: protocol.GoAwayShutdown})
			if kind == "malformed" {
				f.Payload = []byte(`{"code":"private-invalid-value"}`)
			}
			if kind == "premature complete" {
				f, _ = protocol.EncodeGoAway(protocol.GoAway{Code: protocol.GoAwayDrained})
			}
			sent := make(chan struct{})
			go func() { defer close(sent); _ = f.Encode(b) }()
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("invalid GOAWAY accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("invalid frame stranded reader")
			}
			b.Close()
			<-sent
		})
	}
}

func TestDrainingDoesNotHideInvalidFrameHeaders(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	o := Options{MaxStreams: 1, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: time.Now().Add(time.Minute), Diagnostic: true, GracefulShutdown: true}
	c, err := New(context.Background(), a, a, o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- c.Run() }()
	shutdown, _ := protocol.EncodeGoAway(protocol.GoAway{Code: protocol.GoAwayShutdown})
	if err = shutdown.Encode(b); err != nil {
		t.Fatal(err)
	}
	// Do not read the response GOAWAY, keeping the drain writer alive while
	// the sole reader encounters an illegal header from the draining peer.
	header := make([]byte, protocol.HeaderSize)
	header[0], header[1], header[2] = protocol.Version, byte(protocol.TypeGoAway), 1
	sent := make(chan struct{})
	go func() { defer close(sent); _, _ = b.Write(header) }()
	select {
	case err = <-done:
		if !errors.Is(err, protocol.ErrInvalidFlags) {
			t.Fatalf("drain hid terminal header error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("malformed draining peer stranded workers")
	}
	b.Close()
	<-sent
}
