package mux

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

func pair(t *testing.T, accept func(*Stream, protocol.OpenStream)) (*Conn, *Conn) {
	return pairWithLimit(t, 2, accept)
}
func pairWithLimit(t *testing.T, limit int, accept func(*Stream, protocol.OpenStream)) (*Conn, *Conn) {
	t.Helper()
	a, b := net.Pipe()
	options := Options{MaxStreams: limit, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: 5 * time.Second, WriteTimeout: time.Second, IdleTimeout: 5 * time.Second, ExpiresAt: time.Now().Add(time.Minute)}
	origin, err := New(context.Background(), a, a, options)
	if err != nil {
		t.Fatal(err)
	}
	options.Accept = accept
	remote, err := New(context.Background(), b, b, options)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- origin.Run() }()
	go func() { done <- remote.Run() }()
	t.Cleanup(func() {
		origin.Close()
		remote.Close()
		for range 2 {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("mux worker did not terminate")
			}
		}
	})
	return origin, remote
}
func request() protocol.OpenStream {
	return protocol.OpenStream{Method: "POST", Target: "/", Host: "p-abc.portway.localhost", Headers: [][]string{}, ContentLength: -1}
}
func TestMuxStreamsFragmentAndHalfClose(t *testing.T) {
	origin, _ := pair(t, func(s *Stream, _ protocol.OpenStream) {
		if err := s.Accept(); err != nil {
			return
		}
		_, _ = io.Copy(s, s)
		_ = s.CloseWrite()
	})
	stream, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	payload := strings.Repeat("stream-data", 10000)
	written := make(chan error, 1)
	go func() {
		_, err := io.Copy(stream, strings.NewReader(payload))
		if err == nil {
			err = stream.CloseWrite()
		}
		written <- err
	}()
	got, err := io.ReadAll(stream)
	if err != nil || string(got) != payload {
		t.Fatal("fragmented stream changed bytes")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if origin.ActiveStreams() != 0 {
		t.Fatal("completed stream retained resources")
	}
}
func TestMuxCancellationUnblocksSlowDelivery(t *testing.T) {
	workerDone := make(chan struct{})
	origin, _ := pair(t, func(s *Stream, _ protocol.OpenStream) {
		defer close(workerDone)
		if s.Accept() != nil {
			return
		}
		_, _ = s.Write(make([]byte, protocol.MaxDataSize))
		<-s.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := origin.Open(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := stream.Read(make([]byte, 1)); err == nil {
		t.Fatal("cancelled pipe kept reading")
	}
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("RESET did not cancel remote worker")
	}
}

func TestMuxPreservesRemoteRejectionCode(t *testing.T) {
	origin, _ := pair(t, func(s *Stream, _ protocol.OpenStream) { s.Reject(protocol.StreamLimit) })
	for range 50 {
		_, err := origin.Open(context.Background(), request())
		var remote *RemoteError
		if !errors.As(err, &remote) || remote.Code != protocol.StreamLimit {
			t.Fatalf("remote stream capacity reason lost: %v", err)
		}
	}
}
func TestMuxRejectsStateViolationsBeforeDataDelivery(t *testing.T) {
	for _, typ := range []protocol.Type{protocol.TypeData, protocol.TypeOpenStreamOK, protocol.TypeCloseStream} {
		t.Run(typName(typ), func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			options := Options{MaxStreams: 1, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: time.Now().Add(time.Minute)}
			connection, err := New(context.Background(), a, a, options)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- connection.Run() }()
			frame := protocol.Frame{Type: typ, StreamID: 1, Payload: []byte("{}")} // never opened
			if err := frame.Encode(b); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrProtocol) {
					t.Fatal("unknown stream state accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("state rejection blocked")
			}
		})
	}
}
func typName(t protocol.Type) string {
	switch t {
	case protocol.TypeData:
		return "DATA"
	case protocol.TypeCloseStream:
		return "CLOSE"
	}
	return "ACK"
}
