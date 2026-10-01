package mux

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/radityama/portway/internal/protocol"
)

type Stream struct {
	parent     *Conn
	id         uint64
	ctx        context.Context
	cancel     context.CancelFunc
	stop       func() bool
	reader     *io.PipeReader
	inbound    *io.PipeWriter
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
}

func (s *Stream) Context() context.Context { return s.ctx }
func (s *Stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	ended, err := s.ended, s.endErr
	s.mu.Unlock()
	if ended {
		return 0, err
	}
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	return s.reader.Read(p)
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
		n := min(len(p), int(size))
		f := protocol.Frame{Type: protocol.TypeData, StreamID: s.id, Payload: p[:n]}
		if err := s.parent.write(s.ctx, f); err != nil {
			return total, err
		}
		total += n
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
	_ = s.parent.control(s.parent.ctx, protocol.TypeOpenStreamError, s.id, code)
	s.finish(&RemoteError{Code: code})
}
func (s *Stream) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	if s.sendClosed || s.ended {
		s.mu.Unlock()
		return net.ErrClosed
	}
	s.sendClosed = true
	s.mu.Unlock()
	return s.parent.control(s.ctx, protocol.TypeCloseStream, s.id, "")
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
		s.reader.CloseWithError(err)
		s.inbound.CloseWithError(err)
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
		_ = s.parent.control(s.parent.ctx, protocol.TypeResetStream, s.id, code)
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
