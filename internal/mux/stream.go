package mux

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

type Stream struct {
	parent     *Conn
	id         uint64
	ctx        context.Context
	cancel     context.CancelFunc
	stop       func() bool
	ready      chan error
	mu         sync.Mutex
	writeMu    sync.Mutex
	accepted   bool
	sent       bool
	sendClosed bool
	recvClosed bool
	ended      bool
	endErr     error
	once       sync.Once
	// These fields are guarded by parent.mu, never mu.
	sendWindow    uint32
	recvWindow    uint32
	pendingWindow uint32
	pages         []*receivePage
	buffered      int
	lastActivity  time.Time
	idleExpired   bool
}

func (s *Stream) Context() context.Context { return s.ctx }
func (s *Stream) Read(p []byte) (int, error) {
	return s.parent.read(s, p)
}
func (s *Stream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	valid := s.accepted && !s.sendClosed && !s.ended
	s.mu.Unlock()
	if !valid {
		return 0, net.ErrClosed
	}
	total := 0
	size := min(uint32(protocol.MaxDataSize), s.parent.opts.MaxFrame)
	for len(p) > 0 {
		n, err := s.parent.reserve(s, min(len(p), int(size)))
		if err != nil {
			return total, err
		}
		f := protocol.Frame{Type: protocol.TypeData, StreamID: s.id, Payload: p[:n]}
		if err := s.parent.write(s.ctx, f); err != nil {
			s.parent.refundUnsent(s, uint32(n))
			return total, err
		}
		total += n
		s.parent.mu.Lock()
		s.parent.touchLocked(s)
		s.parent.mu.Unlock()
		p = p[n:]
	}
	return total, nil
}
func (s *Stream) Accept() error {
	s.mu.Lock()
	if s.accepted || s.ended {
		s.mu.Unlock()
		return ErrProtocol
	}
	s.accepted = true
	s.mu.Unlock()
	return s.parent.control(s.ctx, protocol.TypeOpenStreamOK, s.id, "")
}
func (s *Stream) Reject(code string) {
	_ = s.parent.queueControl(protocol.TypeOpenStreamError, s.id, code)
	s.finish(&RemoteError{Code: code})
}
func (s *Stream) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	if !s.accepted || s.sendClosed || s.ended {
		s.mu.Unlock()
		return net.ErrClosed
	}
	s.sendClosed = true
	s.mu.Unlock()
	return s.parent.control(s.ctx, protocol.TypeCloseStream, s.id, "")
}

// WaitReceiveClose waits for the peer's directional FIN without consuming data.
// A response sender uses this before releasing a stream, so its cleanup cannot
// reset a response that the peer still has queued for reading.
func (s *Stream) WaitReceiveClose() error {
	c := s.parent
	for {
		c.mu.Lock()
		s.mu.Lock()
		ended, err, closed := s.ended, s.endErr, s.recvClosed
		s.mu.Unlock()
		changed := c.changed
		c.mu.Unlock()
		if ended {
			return err
		}
		if closed {
			return nil
		}
		select {
		case <-changed:
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
	}
}
func (s *Stream) finish(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.ended = true
		s.endErr = err
		stop := s.stop
		s.mu.Unlock()
		if stop != nil {
			stop()
		}
		s.cancel()
		select {
		case s.ready <- err:
		default:
		}
		s.parent.remove(s)
	})
}
func (s *Stream) Reset(code string) {
	s.mu.Lock()
	ended := s.ended
	sent := s.sent
	s.mu.Unlock()
	if ended {
		return
	}
	s.finish(&RemoteError{Code: code})
	if sent {
		_ = s.parent.queueControl(protocol.TypeResetStream, s.id, code)
	}
}
func (s *Stream) Close() error {
	s.mu.Lock()
	complete := s.sendClosed && s.recvClosed
	s.mu.Unlock()
	if complete {
		s.finish(io.EOF)
	} else {
		s.Reset(protocol.StreamCancelled)
	}
	return nil
}
