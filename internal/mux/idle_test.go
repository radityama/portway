package mux

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

func TestStreamingIdleRefreshAndHeartbeatDoesNotKeepStreamAlive(t *testing.T) {
	origin, remote := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() != nil {
			return
		}
		_, _ = io.Copy(s, s)
		_ = s.CloseWrite()
	}, func(o *Options) { o.Streaming = true; o.StreamTimeout = 160 * time.Millisecond })
	stream, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	for range 12 {
		if _, err := stream.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		var reply [1]byte
		if _, err := io.ReadFull(stream, reply[:]); err != nil || reply[0] != 'x' {
			t.Fatal("active stream timed out")
		}
		time.Sleep(40 * time.Millisecond)
	}
	_, err = stream.Read(make([]byte, 1))
	var timeout *RemoteError
	if !errors.As(err, &timeout) || timeout.Code != protocol.StreamTimeout {
		t.Fatalf("idle stream=%v", err)
	}
	if origin.ctx.Err() != nil || remote.ctx.Err() != nil {
		t.Fatal("stream idle killed healthy tunnel")
	}
	eventuallyStreams(t, origin, remote)
}

func eventuallyStreams(t *testing.T, peers ...*Conn) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		active := 0
		for _, c := range peers {
			active += c.ActiveStreams()
		}
		if active == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("idle stream leaked")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLegacyStreamsKeepWholeRequestDeadline(t *testing.T) {
	origin, _ := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() != nil {
			return
		}
		_, _ = io.Copy(s, s)
	}, func(o *Options) { o.StreamTimeout = 120 * time.Millisecond })
	stream, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for {
		_, err = stream.Write([]byte("x"))
		if err != nil {
			break
		}
		_, err = io.ReadFull(stream, make([]byte, 1))
		if err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
		if time.Since(start) > time.Second {
			t.Fatal("legacy deadline was extended")
		}
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("legacy whole timeout changed")
	}
}

func TestWebSocketCannotOpenWithoutNegotiatedCapability(t *testing.T) {
	c, _ := pair(t, func(s *Stream, _ protocol.OpenStream) { _ = s.Accept() })
	open := request()
	open.Upgrade = "websocket"
	if _, err := c.Open(context.Background(), open); !errors.Is(err, ErrProtocol) {
		t.Fatal("unnegotiated upgrade opened")
	}
	if c.ActiveStreams() != 0 {
		t.Fatal("rejected upgrade consumed capacity")
	}
}

func TestStreamingActivityCannotExtendCredentialExpiry(t *testing.T) {
	origin, _ := drainPair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() != nil {
			return
		}
		_, _ = io.Copy(s, s)
	}, func(o *Options) {
		o.Streaming = true
		o.StreamTimeout = time.Second
		o.ExpiresAt = time.Now().Add(180 * time.Millisecond)
	})
	stream, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for {
		if _, err = stream.Write([]byte("x")); err != nil {
			break
		}
		if _, err = io.ReadFull(stream, make([]byte, 1)); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
		if time.Since(start) > time.Second {
			t.Fatal("activity extended credential expiry")
		}
	}
	select {
	case <-origin.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("credential did not cancel streaming parent")
	}
}
