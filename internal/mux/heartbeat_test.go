package mux

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

func heartbeatOptions() Options {
	return Options{MaxStreams: 1, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: time.Now().Add(time.Minute), Heartbeat: true, HeartbeatInterval: 10 * time.Millisecond, HeartbeatTimeout: 100 * time.Millisecond}
}

func heartbeatPeer(t *testing.T, options Options) (*Conn, net.Conn, <-chan error) {
	t.Helper()
	a, b := net.Pipe()
	c, err := New(context.Background(), a, a, options)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Run() }()
	t.Cleanup(func() { c.Close(); b.Close() })
	if err := b.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return c, b, done
}

func awaitHeartbeatExit(t *testing.T, done <-chan error, expected error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, expected) {
			t.Fatalf("heartbeat exit: %v, want %v", err, expected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat workers did not stop")
	}
}

func TestHeartbeatTimeoutDespiteOtherTraffic(t *testing.T) {
	options := heartbeatOptions()
	options.Diagnostic = true
	c, peer, done := heartbeatPeer(t, options)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, err := protocol.Decode(peer); err != nil {
				return
			}
		}
	}()
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		f, _ := protocol.EncodeHeartbeat(protocol.TypePing, protocol.Heartbeat{Nonce: "0123456789abcdef", Timestamp: "2026-10-01T00:00:00Z"})
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				if f.Encode(peer) != nil {
					return
				}
			}
		}
	}()
	awaitHeartbeatExit(t, done, ErrHeartbeatTimeout)
	peer.Close()
	for _, stopped := range []<-chan struct{}{readDone, writeDone} {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("test peer did not stop")
		}
	}
}

func TestHeartbeatRejectsWrongAndDuplicatePong(t *testing.T) {
	for _, mode := range []string{"nonce", "timestamp", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			_, peer, done := heartbeatPeer(t, heartbeatOptions())
			ping, err := protocol.DecodeTypes(peer, protocol.MaxHandshakePayloadSize, protocol.TypePing)
			if err != nil {
				t.Fatal(err)
			}
			value, err := protocol.DecodeHeartbeat(ping)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "nonce" {
				value.Nonce = "0000000000000000"
			}
			if mode == "timestamp" {
				value.Timestamp = "2026-10-01T00:00:00Z"
			}
			pong, _ := protocol.EncodeHeartbeat(protocol.TypePong, value)
			if pong.Encode(peer) != nil {
				t.Fatal("could not send PONG")
			}
			if mode == "duplicate" {
				_ = pong.Encode(peer)
			}
			awaitHeartbeatExit(t, done, protocol.ErrInvalidHeartbeat)
		})
	}
}

func TestHeartbeatRejectsUnsolicitedMalformedAndUnnegotiatedFrames(t *testing.T) {
	for _, mode := range []string{"unsolicited", "malformed", "unnegotiated", "diagnostic_data"} {
		t.Run(mode, func(t *testing.T) {
			options := heartbeatOptions()
			options.HeartbeatInterval = time.Second
			options.Diagnostic = true
			if mode == "unnegotiated" {
				options.Heartbeat = false
			}
			_, peer, done := heartbeatPeer(t, options)
			f, _ := protocol.EncodeHeartbeat(protocol.TypePong, protocol.Heartbeat{Nonce: "0123456789abcdef", Timestamp: "2026-10-01T00:00:00Z"})
			want := protocol.ErrInvalidHeartbeat
			switch mode {
			case "malformed":
				f.Payload = []byte("{}")
			case "unnegotiated":
				want = protocol.ErrUnexpectedType
			case "diagnostic_data":
				f = protocol.Frame{Type: protocol.TypeData, StreamID: 1, Payload: []byte("payload")}
				want = protocol.ErrUnexpectedType
			}
			_ = f.Encode(peer)
			awaitHeartbeatExit(t, done, want)
		})
	}
}

func TestHeartbeatWorksWhileStreamHasNoCreditAndJoinsOnClose(t *testing.T) {
	a, b := net.Pipe()
	options := heartbeatOptions()
	origin, err := New(context.Background(), a, a, options)
	if err != nil {
		t.Fatal(err)
	}
	options.Accept = func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() == nil {
			<-s.Context().Done()
		}
	}
	remote, err := New(context.Background(), b, b, options)
	if err != nil {
		t.Fatal(err)
	}
	defer origin.Close()
	defer remote.Close()
	done := make(chan error, 2)
	go func() { done <- origin.Run() }()
	go func() { done <- remote.Run() }()
	s, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(make([]byte, protocol.InitialStreamWindow)); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() { _, err := s.Write([]byte("blocked")); writeDone <- err }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for origin.Liveness().LastPong.IsZero() || remote.Liveness().LastPong.IsZero() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("zero stream credit blocked heartbeat")
		}
	}
	select {
	case <-writeDone:
		t.Fatal("stream escaped credit bound")
	default:
	}
	origin.Close()
	remote.Close()
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked DATA survived connection close")
		}
	case <-time.After(time.Second):
		t.Fatal("credit waiter retained")
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("heartbeat/stream worker retained")
		}
	}
	if origin.ActiveStreams() != 0 || remote.ActiveStreams() != 0 {
		t.Fatal("closed connection retained streams")
	}
}

func TestHeartbeatNeverExtendsCredentialDeadline(t *testing.T) {
	options := heartbeatOptions()
	options.ExpiresAt = time.Now().Add(40 * time.Millisecond)
	_, peer, done := heartbeatPeer(t, options)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, err := protocol.Decode(peer); err != nil {
				return
			}
		}
	}()
	awaitHeartbeatExit(t, done, context.DeadlineExceeded)
	peer.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("expiry retained heartbeat peer")
	}
}

func TestHeartbeatBlockedProbeWriteHasDeadline(t *testing.T) {
	options := heartbeatOptions()
	options.WriteTimeout = 20 * time.Millisecond
	_, _, done := heartbeatPeer(t, options)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked probe write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("probe writer has no deadline")
	}
}
