// Package mux owns bounded HTTP streams on one authenticated tunnel socket.
package mux

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

var ErrLimit = errors.New("tunnel stream capacity reached")
var ErrProtocol = errors.New("invalid tunnel stream state")

type RemoteError struct{ Code string }

func (e *RemoteError) Error() string { return "stream failed: " + e.Code }

type Options struct {
	MaxStreams        int
	MaxFrame          uint32
	StreamTimeout     time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ExpiresAt         time.Time
	Accept            func(*Stream, protocol.OpenStream)
	Diagnostic        bool
	Heartbeat         bool
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	GracefulShutdown  bool
	ShutdownTimeout   time.Duration
	Shutdown          <-chan struct{}
	OnDraining        func()
	Streaming         bool
	WebSocket         bool
	CustomDomains     bool
}
type Conn struct {
	conn      net.Conn
	reader    io.Reader
	opts      Options
	ctx       context.Context
	parent    context.Context
	cancel    context.CancelFunc
	stop      func() bool
	writer    chan struct{}
	opening   chan struct{}
	mu        sync.Mutex
	streams   map[uint64]*Stream
	highest   uint64
	closed    bool
	once      sync.Once
	workers   sync.WaitGroup
	accepting int
	// Credit, receive buffers, and their notifications are all owned by mu.
	sendWindow        uint32
	recvWindow        uint32
	pendingConnection uint32
	queuedBytes       int
	freePages         []*receivePage
	allocatedPages    int
	changed           chan struct{}
	controlWake       chan struct{}
	controls          chan protocol.Frame
	probe             *heartbeatProbe
	lastPong          time.Time
	heartbeatWake     chan struct{}
	terminalError     error
	draining          bool
	peerDraining      bool
	peerDrained       bool
	drainedStarted    bool
	drainedSent       bool
	drainStarted      bool
	drainDeadline     time.Time
	drainWake         chan struct{}
	drainWriteDone    chan struct{}
	drainWriteErr     error
	idleWake          chan struct{}
}

func New(ctx context.Context, conn net.Conn, reader io.Reader, opts Options) (*Conn, error) {
	if opts.WebSocket && !opts.Streaming {
		return nil, ErrProtocol
	}
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 10 * time.Second
	}
	if opts.ShutdownTimeout <= 0 || opts.ShutdownTimeout > time.Minute {
		return nil, ErrProtocol
	}
	if opts.HeartbeatInterval == 0 {
		opts.HeartbeatInterval = protocol.HeartbeatInterval
	}
	if opts.HeartbeatTimeout == 0 {
		opts.HeartbeatTimeout = protocol.HeartbeatTimeout
	}
	if opts.HeartbeatInterval <= 0 || opts.HeartbeatTimeout <= 0 {
		return nil, ErrProtocol
	}
	if conn == nil || reader == nil || opts.MaxStreams < 1 || opts.MaxStreams > 1024 || opts.MaxFrame < 4096 || opts.MaxFrame > protocol.MaxPayloadSize || opts.StreamTimeout <= 0 || opts.WriteTimeout <= 0 || opts.IdleTimeout <= 0 || opts.ExpiresAt.IsZero() {
		return nil, ErrProtocol
	}
	life, cancel := context.WithDeadline(ctx, opts.ExpiresAt)
	c := &Conn{conn: conn, reader: reader, opts: opts, ctx: life, parent: ctx, cancel: cancel, writer: make(chan struct{}, 1), opening: make(chan struct{}, 1), streams: make(map[uint64]*Stream), sendWindow: protocol.InitialConnectionWindow, recvWindow: protocol.InitialConnectionWindow, changed: make(chan struct{}), controlWake: make(chan struct{}, 1), controls: make(chan protocol.Frame, 2*opts.MaxStreams+4), heartbeatWake: make(chan struct{}, 1)}
	c.mu.Lock()
	c.drainWake = make(chan struct{}, 1)
	c.idleWake = make(chan struct{}, 1)
	c.drainWriteDone = make(chan struct{})
	c.stop = context.AfterFunc(life, c.Close)
	c.mu.Unlock()
	return c, nil
}
func (c *Conn) ActiveStreams() int  { c.mu.Lock(); defer c.mu.Unlock(); return len(c.streams) }
func (c *Conn) Streaming() bool     { return c.opts.Streaming }
func (c *Conn) CustomDomains() bool { return c.opts.CustomDomains }
func (c *Conn) WebSocket() bool     { return c.opts.WebSocket }
func (c *Conn) Close() {
	c.once.Do(func() {
		c.mu.Lock()
		stop := c.stop
		c.mu.Unlock()
		c.cancel()
		if stop != nil {
			stop()
		}
		transport.Close(c.conn)
		c.mu.Lock()
		c.closed = true
		c.notifyLocked()
		streams := c.streams
		c.streams = make(map[uint64]*Stream)
		c.mu.Unlock()
		for _, s := range streams {
			s.finish(net.ErrClosed)
		}
		c.mu.Lock()
		c.freePages = nil
		c.allocatedPages = 0
		c.pendingConnection = 0
		c.mu.Unlock()
	})
}
func (c *Conn) remove(s *Stream) {
	c.mu.Lock()
	if c.streams[s.id] == s {
		delete(c.streams, s.id)
	}
	c.discardLocked(s)
	c.notifyLocked()
	c.mu.Unlock()
}
func (c *Conn) newStreamLocked(ctx context.Context, id uint64) *Stream {
	var life context.Context
	var cancel context.CancelFunc
	if c.opts.Streaming {
		life, cancel = context.WithCancel(ctx)
	} else {
		life, cancel = context.WithTimeout(ctx, c.opts.StreamTimeout)
	}
	s := &Stream{parent: c, id: id, ctx: life, cancel: cancel, ready: make(chan error, 1), sendWindow: protocol.InitialStreamWindow, recvWindow: protocol.InitialStreamWindow}
	c.streams[id] = s
	c.touchLocked(s)
	s.mu.Lock()
	s.stop = context.AfterFunc(life, func() {
		code := protocol.StreamCancelled
		if errors.Is(life.Err(), context.DeadlineExceeded) {
			code = protocol.StreamTimeout
		}
		s.Reset(code)
	})
	s.mu.Unlock()
	return s
}
func (c *Conn) write(ctx context.Context, frame protocol.Frame) error {
	if frame.Version == 0 {
		frame.Version = protocol.Version
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	if len(frame.Payload) > int(c.opts.MaxFrame) {
		return protocol.ErrPayloadTooLarge
	}
	writeCtx, cancel := context.WithTimeout(ctx, c.opts.WriteTimeout)
	defer cancel()
	select {
	case c.writer <- struct{}{}:
	case <-writeCtx.Done():
		return writeCtx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
	defer func() { <-c.writer }()
	if err := writeCtx.Err(); err != nil {
		return err
	}
	deadline, _ := writeCtx.Deadline()
	if d, ok := c.ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		c.Close()
		return err
	}
	if err := frame.EncodeWithLimit(c.conn, c.opts.MaxFrame); err != nil {
		c.Close()
		return err
	}
	return nil
}
func (c *Conn) control(ctx context.Context, typ protocol.Type, id uint64, code string) error {
	f, err := protocol.EncodeStreamControl(typ, id, code)
	if err != nil {
		return err
	}
	return c.write(ctx, f)
}
func (c *Conn) startOpen(ctx context.Context, request protocol.OpenStream) (*Stream, error) {
	if (request.PublicHost != "" && !c.opts.CustomDomains) || request.Upgrade != "" && !c.opts.WebSocket {
		return nil, ErrProtocol
	}
	if c.opts.Accept != nil || c.opts.Diagnostic {
		return nil, ErrProtocol
	}
	// Allocation order must also be wire order. Release this token before
	// waiting for ACK so independent stream handshakes can remain concurrent.
	waitCtx, cancel := context.WithTimeout(ctx, c.opts.WriteTimeout)
	defer cancel()
	select {
	case c.opening <- struct{}{}:
	case <-waitCtx.Done():
		return nil, waitCtx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
	defer func() { <-c.opening }()
	if err := waitCtx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	if c.draining {
		c.mu.Unlock()
		return nil, ErrDraining
	}
	if len(c.streams) >= c.opts.MaxStreams || c.highest == ^uint64(0) {
		c.mu.Unlock()
		return nil, ErrLimit
	}
	c.highest++
	s := c.newStreamLocked(ctx, c.highest)
	c.mu.Unlock()
	f, err := protocol.EncodeOpenStream(s.id, request)
	if err == nil {
		err = c.write(s.ctx, f)
	}
	if err != nil {
		s.finish(err)
		return nil, err
	}
	s.mu.Lock()
	s.sent = true
	ended := s.ended
	endErr := s.endErr
	s.mu.Unlock()
	if ended {
		_ = c.queueControl(protocol.TypeResetStream, s.id, protocol.StreamCancelled)
		return nil, endErr
	}
	return s, nil
}

func (c *Conn) Open(ctx context.Context, request protocol.OpenStream) (*Stream, error) {
	s, err := c.startOpen(ctx, request)
	if err != nil {
		return nil, err
	}
	select {
	case err := <-s.ready:
		if err != nil {
			s.finish(err)
			return nil, err
		}
		return s, nil
	case <-s.ctx.Done():
		// Remote rejection also cancels the stream. Preserve its stable reason
		// regardless of whether this select observes cancellation or ready first.
		s.mu.Lock()
		ended, endErr := s.ended, s.endErr
		s.mu.Unlock()
		if ended {
			return nil, endErr
		}
		err := s.ctx.Err()
		code := protocol.StreamCancelled
		if errors.Is(err, context.DeadlineExceeded) {
			code = protocol.StreamTimeout
		}
		s.Reset(code)
		return nil, err
	case <-c.ctx.Done():
		s.finish(c.ctx.Err())
		return nil, c.ctx.Err()
	}
}

// Run is the only reader. It joins bounded acceptance workers before returning.
func (c *Conn) Run() (result error) {
	// A control callback can observe cancellation before the socket reader.
	// Preserve the owned worker's terminal reason on either exit path.
	defer func() { result = c.readError(result); c.Close(); c.workers.Wait() }()
	c.workers.Add(1)
	go func() { defer c.workers.Done(); c.controlLoop() }()
	if c.opts.Streaming {
		c.workers.Add(1)
		go func() { defer c.workers.Done(); c.streamIdleLoop() }()
	}
	if c.opts.Heartbeat {
		c.workers.Add(1)
		go func() { defer c.workers.Done(); c.heartbeatLoop() }()
	}
	if c.opts.GracefulShutdown || c.opts.Shutdown != nil {
		c.workers.Add(1)
		go func() { defer c.workers.Done(); c.drainLoop() }()
	}
	allowed := []protocol.Type{protocol.TypeData, protocol.TypeWindowUpdate, protocol.TypeCloseStream, protocol.TypeResetStream}
	if c.opts.Accept != nil {
		allowed = append(allowed, protocol.TypeOpenStream)
	} else {
		allowed = append(allowed, protocol.TypeOpenStreamOK, protocol.TypeOpenStreamError)
	}
	if c.opts.Diagnostic {
		allowed = nil
	}
	if c.opts.Heartbeat {
		allowed = append(allowed, protocol.TypePing, protocol.TypePong)
	}
	if c.opts.GracefulShutdown {
		allowed = append(allowed, protocol.TypeGoAway)
	}
	for {
		deadline := time.Now().Add(c.opts.IdleTimeout)
		if d, ok := c.ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			return c.readError(err)
		}
		f, err := protocol.DecodeTypes(c.reader, c.opts.MaxFrame, allowed...)
		if err != nil {
			return c.readError(err)
		}
		if f.Type == protocol.TypePing || f.Type == protocol.TypePong {
			if err := c.receiveHeartbeat(f); err != nil {
				return err
			}
			continue
		}
		if f.Type == protocol.TypeGoAway {
			if err := c.receiveGoAway(f); err != nil {
				return err
			}
			continue
		}
		if f.Type == protocol.TypeOpenStream {
			if c.opts.Accept == nil {
				return ErrProtocol
			}
			request, err := protocol.DecodeOpenStream(f)
			if err != nil {
				return err
			}
			if (request.PublicHost != "" && !c.opts.CustomDomains) || request.Upgrade != "" && !c.opts.WebSocket {
				return ErrProtocol
			}
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return net.ErrClosed
			}
			if f.StreamID <= c.highest {
				c.mu.Unlock()
				return ErrProtocol
			}
			c.highest = f.StreamID
			if c.draining {
				code := protocol.StreamLimit
				if c.opts.GracefulShutdown {
					code = protocol.StreamDraining
				}
				c.mu.Unlock()
				if err := c.queueControl(protocol.TypeOpenStreamError, f.StreamID, code); err != nil {
					return err
				}
				continue
			}
			if len(c.streams) >= c.opts.MaxStreams || c.accepting >= c.opts.MaxStreams {
				c.mu.Unlock()
				if err := c.queueControl(protocol.TypeOpenStreamError, f.StreamID, protocol.StreamLimit); err != nil {
					return err
				}
				continue
			}
			s := c.newStreamLocked(c.ctx, f.StreamID)
			s.mu.Lock()
			s.sent = true
			s.mu.Unlock()
			c.accepting++
			c.mu.Unlock()
			c.workers.Add(1)
			go func() {
				defer c.workers.Done()
				defer func() {
					s.Close()
					c.mu.Lock()
					c.accepting--
					c.notifyLocked()
					c.mu.Unlock()
				}()
				c.opts.Accept(s, request)
			}()
			continue
		}
		if f.Type == protocol.TypeWindowUpdate {
			if err := c.applyWindow(f); err != nil {
				return err
			}
			continue
		}
		if f.Type == protocol.TypeData {
			if err := c.receiveData(f.StreamID, f.Payload); err != nil {
				return err
			}
			continue
		}
		c.mu.Lock()
		s := c.streams[f.StreamID]
		highest := c.highest
		c.mu.Unlock()
		if s == nil {
			if f.StreamID > highest {
				return ErrProtocol
			}
			continue
		}
		code, err := protocol.DecodeStreamControl(f)
		if err != nil {
			return err
		}
		switch f.Type {
		case protocol.TypeOpenStreamOK:
			s.mu.Lock()
			ended := s.ended
			valid := c.opts.Accept == nil && !s.accepted && !s.ended
			if valid {
				s.accepted = true
			}
			s.mu.Unlock()
			if !valid {
				if ended {
					continue
				}
				return ErrProtocol
			}
			select {
			case s.ready <- nil:
			default: // Cancellation may already have supplied the terminal reason.
			}
		case protocol.TypeOpenStreamError:
			s.mu.Lock()
			ended := s.ended
			valid := c.opts.Accept == nil && !s.accepted
			s.mu.Unlock()
			if !valid {
				if ended {
					continue
				}
				return ErrProtocol
			}
			s.finish(&RemoteError{Code: code})
		case protocol.TypeCloseStream:
			c.mu.Lock()
			s.mu.Lock()
			valid := s.accepted && !s.recvClosed
			if valid {
				s.recvClosed = true
			}
			s.mu.Unlock()
			s.pendingWindow = 0
			c.notifyLocked()
			c.mu.Unlock()
			if !valid {
				return ErrProtocol
			}
		case protocol.TypeResetStream:
			s.finish(&RemoteError{Code: code})
		}
	}
}

func (c *Conn) readError(err error) error {
	// Negotiated draining classifies transport closure only. A bad header must
	// remain a terminal protocol error, even after a valid SHUTDOWN.
	var network net.Error
	transportEnd := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network)
	if !transportEnd {
		return err
	}
	c.mu.Lock()
	terminal := c.terminalError
	peerDraining := c.peerDraining
	c.mu.Unlock()
	if terminal != nil {
		return terminal
	}
	if c.parent.Err() != nil {
		return c.parent.Err()
	}
	// A socket deadline can fire just before the context timer is scheduled.
	// Normalize that race so credential expiry has one terminal session reason.
	if !time.Now().Before(c.opts.ExpiresAt) {
		return context.DeadlineExceeded
	}
	if peerDraining {
		return ErrPeerShutdown
	}
	if c.ctx.Err() != nil {
		if errors.Is(c.ctx.Err(), context.Canceled) {
			return net.ErrClosed
		}
		return c.ctx.Err()
	}
	return err
}
