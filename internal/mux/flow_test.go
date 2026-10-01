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

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("flow-control condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func queued(c *Conn) int { c.mu.Lock(); defer c.mu.Unlock(); return c.queuedBytes }
func waitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("stream operation did not terminate")
		return nil
	}
}
func noResultYet(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("credit-exhausted writer did not wait: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSlowStreamDoesNotBlockFastStream(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	origin, remote := pair(t, func(s *Stream, open protocol.OpenStream) {
		if s.Accept() != nil {
			return
		}
		if open.Target == "/slow" {
			select {
			case <-release:
			case <-s.Context().Done():
				return
			}
		}
		_, _ = io.Copy(s, s)
		_ = s.CloseWrite()
	})
	slowRequest := request()
	slowRequest.Target = "/slow"
	slow, err := origin.Open(context.Background(), slowRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	written := make(chan error, 1)
	go func() { _, err := slow.Write(make([]byte, 2*protocol.InitialStreamWindow)); written <- err }()
	waitFor(t, func() bool { return queued(remote) == int(protocol.InitialStreamWindow) })
	noResultYet(t, written)
	fast, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	if _, err := fast.Write([]byte("quick")); err != nil {
		t.Fatal(err)
	}
	if err := fast.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(fast)
	if err != nil || string(data) != "quick" {
		t.Fatal("stalled stream blocked or corrupted another stream")
	}
	slow.Reset(protocol.StreamCancelled)
	if waitResult(t, written) == nil {
		t.Fatal("reset failed to interrupt credit wait")
	}
	waitFor(t, func() bool { return queued(remote) == 0 })
}

func TestConnectionBudgetAndDiscardReturnCredit(t *testing.T) {
	origin, remote := pairWithLimit(t, 32, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() == nil {
			<-s.Context().Done()
		}
	})
	var streams []*Stream
	for range protocol.InitialConnectionWindow / protocol.InitialStreamWindow {
		stream, err := origin.Open(context.Background(), request())
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, stream)
		if _, err := stream.Write(make([]byte, protocol.InitialStreamWindow)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return queued(remote) == int(protocol.InitialConnectionWindow) })
	remote.mu.Lock()
	pages := remote.allocatedPages
	remote.mu.Unlock()
	if pages > int(protocol.InitialConnectionWindow)/receivePageSize+2*remote.opts.MaxStreams {
		t.Fatal("receive-page memory budget exceeded")
	}
	// OPEN/ACK still work at zero DATA credit.
	extra, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal("exhausted DATA credit blocked control traffic")
	}
	defer extra.Close()
	written := make(chan error, 1)
	go func() { _, err := extra.Write([]byte("x")); written <- err }()
	noResultYet(t, written)
	streams[0].Reset(protocol.StreamCancelled)
	if err := waitResult(t, written); err != nil {
		t.Fatal("discarded stream failed to return connection credit")
	}
	waitFor(t, func() bool {
		return queued(remote) == int(protocol.InitialConnectionWindow-protocol.InitialStreamWindow)+1
	})
	for _, stream := range streams {
		stream.Close()
	}
	extra.Close()
	waitFor(t, func() bool {
		origin.mu.Lock()
		defer origin.mu.Unlock()
		return origin.sendWindow == protocol.InitialConnectionWindow
	})
	remote.Close()
	remote.mu.Lock()
	retained := remote.queuedBytes != 0 || remote.allocatedPages != 0 || len(remote.freePages) != 0
	remote.mu.Unlock()
	if retained {
		t.Fatal("connection close retained receive buffers")
	}
}

func TestTinyFramesCoalesceIntoBoundedPages(t *testing.T) {
	origin, remote := pair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() == nil {
			<-s.Context().Done()
		}
	})
	stream, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 * receivePageSize {
		if _, err := stream.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return queued(remote) == 2*receivePageSize })
	remote.mu.Lock()
	allocated := remote.allocatedPages
	remote.mu.Unlock()
	if allocated != 2 {
		t.Fatal("tiny frames retained per-frame receive allocations")
	}
	stream.Close()
}

func TestCreditWaitHonorsCancellationDeadlineAndParentClose(t *testing.T) {
	for _, cause := range []string{"cancel", "deadline", "parent close"} {
		t.Run(cause, func(t *testing.T) {
			origin, remote := pair(t, func(s *Stream, _ protocol.OpenStream) {
				if s.Accept() == nil {
					<-s.Context().Done()
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			if cause == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			}
			defer cancel()
			stream, err := origin.Open(ctx, request())
			if err != nil {
				t.Fatal(err)
			}
			written := make(chan error, 1)
			go func() { _, err := stream.Write(make([]byte, 2*protocol.InitialStreamWindow)); written <- err }()
			waitFor(t, func() bool { return queued(remote) == int(protocol.InitialStreamWindow) })
			noResultYet(t, written)
			if cause == "cancel" {
				cancel()
			}
			if cause == "parent close" {
				origin.Close()
			}
			if waitResult(t, written) == nil {
				t.Fatal("credit wait ignored stream/parent lifetime")
			}
			waitFor(t, func() bool { return queued(remote) == 0 })
		})
	}
}

func TestFinDrainsQueuedDataAndReplenishesCredit(t *testing.T) {
	origin, _ := pair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() != nil {
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
	payload := bytes.Repeat([]byte("flow"), 128*1024)
	done := make(chan error, 1)
	go func() {
		_, err := stream.Write(payload)
		if err == nil {
			err = stream.CloseWrite()
		}
		done <- err
	}()
	data, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("replenished windows or queued FIN changed bytes")
	}
	if err := waitResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestResetDoesNotReleaseWorkerAdmissionBeforeCleanup(t *testing.T) {
	cleanup := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(cleanup) }) }
	defer release()
	origin, remote := pairWithLimit(t, 1, func(s *Stream, open protocol.OpenStream) {
		if s.Accept() != nil {
			return
		}
		<-s.Context().Done()
		if open.Target == "/cleanup" {
			<-cleanup
		}
	})
	open := request()
	open.Target = "/cleanup"
	first, err := origin.Open(context.Background(), open)
	if err != nil {
		t.Fatal(err)
	}
	first.Reset(protocol.StreamCancelled)
	waitFor(t, func() bool { return remote.ActiveStreams() == 0 })
	_, err = origin.Open(context.Background(), request())
	var rejection *RemoteError
	if !errors.As(err, &rejection) || rejection.Code != protocol.StreamLimit {
		t.Fatal("reset admitted another worker before the first worker finished")
	}
	release()
	waitFor(t, func() bool { remote.mu.Lock(); defer remote.mu.Unlock(); return remote.accepting == 0 })
	last, err := origin.Open(context.Background(), request())
	if err != nil {
		t.Fatal("finished cleanup retained worker capacity")
	}
	last.Close()
}

func TestCancelledWriterRefundsUnsentConnectionCredit(t *testing.T) {
	origin, _ := pair(t, func(s *Stream, _ protocol.OpenStream) {
		if s.Accept() == nil {
			<-s.Context().Done()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := origin.Open(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	// Hold the writer after OPEN/ACK to distinguish reserved bytes from bytes
	// actually written to the peer. Cancellation must refund only unsent bytes.
	origin.writer <- struct{}{}
	var once sync.Once
	release := func() { once.Do(func() { <-origin.writer }) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := stream.Write([]byte("unsent")); done <- err }()
	waitFor(t, func() bool {
		origin.mu.Lock()
		defer origin.mu.Unlock()
		return origin.sendWindow == protocol.InitialConnectionWindow-6
	})
	cancel()
	if waitResult(t, done) == nil {
		t.Fatal("cancelled writer unexpectedly sent data")
	}
	release()
	origin.mu.Lock()
	remaining := origin.sendWindow
	origin.mu.Unlock()
	if remaining != protocol.InitialConnectionWindow {
		t.Fatal("unsent DATA leaked connection credit")
	}
}

func TestLateDataForReleasedStreamReturnsConnectionCredit(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	options := Options{MaxStreams: 1, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: time.Now().Add(time.Minute)}
	conn, err := New(context.Background(), a, a, options)
	if err != nil {
		t.Fatal(err)
	}
	conn.highest = 1 // Stream 1 is already released; no tombstone is retained.
	done := make(chan error, 1)
	go func() { done <- conn.Run() }()
	defer conn.Close()
	if err := (protocol.Frame{Type: protocol.TypeData, StreamID: 1, Payload: []byte("late")}).Encode(b); err != nil {
		t.Fatal(err)
	}
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := protocol.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	delta, err := protocol.DecodeWindowUpdate(frame)
	if err != nil || frame.StreamID != 0 || delta != 4 {
		t.Fatal("late DATA lost aggregate connection credit")
	}
	update, _ := protocol.EncodeWindowUpdate(1, 4)
	if err := update.Encode(b); err != nil {
		t.Fatal(err)
	}
	b.Close()
	_ = waitResult(t, done)
	if queued(conn) != 0 {
		t.Fatal("late DATA retained a receive buffer")
	}
}

func TestConcurrentOpenPreservesMonotonicWireIDs(t *testing.T) {
	origin, _ := pairWithLimit(t, 64, func(s *Stream, _ protocol.OpenStream) { s.Reject(protocol.StreamLimit) })
	start := make(chan struct{})
	results := make(chan error, 64)
	for range cap(results) {
		go func() {
			<-start
			_, err := origin.Open(context.Background(), request())
			results <- err
		}()
	}
	close(start)
	for range cap(results) {
		err := waitResult(t, results)
		var remote *RemoteError
		if !errors.As(err, &remote) || remote.Code != protocol.StreamLimit {
			t.Fatalf("concurrent OPEN reordered IDs or lost rejection: %v", err)
		}
	}
}

func TestMalformedCreditAndOverWindowDataCloseConnection(t *testing.T) {
	window := func(id uint64, delta uint32) protocol.Frame {
		frame, err := protocol.EncodeWindowUpdate(id, delta)
		if err != nil {
			t.Fatal(err)
		}
		return frame
	}
	cases := []struct {
		name     string
		streams  int
		accepted bool
		frames   []protocol.Frame
		want     error
	}{
		{"connection overflow", 0, false, []protocol.Frame{window(0, 1)}, ErrProtocol},
		{"stream overflow", 1, true, []protocol.Frame{window(1, 1)}, ErrProtocol},
		{"future ID", 0, false, []protocol.Frame{window(1, 1)}, ErrProtocol},
		{"before ACK", 1, false, []protocol.Frame{window(1, 1)}, ErrProtocol},
		{"zero increment", 0, false, []protocol.Frame{{Type: protocol.TypeWindowUpdate, Payload: make([]byte, 4)}}, protocol.ErrInvalidWindow},
	}
	for _, entry := range []struct {
		name    string
		streams int
	}{{"stream overrun", 1}, {"connection overrun", 17}} {
		var frames []protocol.Frame
		for id := 1; id <= entry.streams; id++ {
			for range 4 {
				frames = append(frames, protocol.Frame{Type: protocol.TypeData, StreamID: uint64(id), Payload: make([]byte, protocol.MaxDataSize)})
			}
		}
		if entry.streams == 1 {
			frames = append(frames, protocol.Frame{Type: protocol.TypeData, StreamID: 1, Payload: []byte("x")})
		}
		cases = append(cases, struct {
			name     string
			streams  int
			accepted bool
			frames   []protocol.Frame
			want     error
		}{entry.name, entry.streams, true, frames, ErrProtocol})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			options := Options{MaxStreams: 32, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: 5 * time.Second, WriteTimeout: time.Second, IdleTimeout: 5 * time.Second, ExpiresAt: time.Now().Add(time.Minute)}
			conn, err := New(context.Background(), a, a, options)
			if err != nil {
				t.Fatal(err)
			}
			conn.mu.Lock()
			for id := 1; id <= test.streams; id++ {
				s := conn.newStreamLocked(conn.ctx, uint64(id))
				s.accepted, s.sent = test.accepted, true
				conn.highest = uint64(id)
			}
			conn.mu.Unlock()
			done := make(chan error, 1)
			go func() { done <- conn.Run() }()
			defer conn.Close()
			for _, frame := range test.frames {
				if err := frame.Encode(b); err != nil {
					break
				}
			}
			if err := waitResult(t, done); !errors.Is(err, test.want) {
				t.Fatalf("unsafe peer was not rejected: %v", err)
			}
		})
	}
}
