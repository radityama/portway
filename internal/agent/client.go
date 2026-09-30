package agent

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

var ErrConfig = errors.New("invalid agent connection configuration")
var ErrAuthentication = errors.New("relay authentication failed")
var ErrSessionInUse = errors.New("session wait may only be called once")

type AuthenticationError struct{ Code string }

func (e *AuthenticationError) Error() string { return "relay authentication failed: " + e.Code }
func (e *AuthenticationError) Unwrap() error { return ErrAuthentication }

type Client struct {
	Transport        transport.Transport
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	WriteTimeout     time.Duration
	MaxFrame         uint32
}

func NewClient(dialer transport.Transport) *Client {
	return &Client{Transport: dialer, HandshakeTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, WriteTimeout: 5 * time.Second, MaxFrame: protocol.MaxPayloadSize}
}

type Session struct {
	ConnectionID   string
	ExpiresAt      time.Time
	MaxPayloadSize uint32
	conn           net.Conn
	reader         *bufio.Reader
	ctx            context.Context
	idleTimeout    time.Duration
	stop           func() bool
	closeOnce      sync.Once
	waiting        atomic.Bool
}

func (c *Client) Connect(ctx context.Context, address, token string) (*Session, error) {
	if c.Transport == nil || c.HandshakeTimeout <= 0 || c.IdleTimeout <= 0 || c.WriteTimeout <= 0 || c.MaxFrame < protocol.MaxHandshakePayloadSize || c.MaxFrame > protocol.MaxPayloadSize || !protocol.ValidToken(token) {
		return nil, ErrConfig
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, c.HandshakeTimeout)
	defer cancel()
	conn, err := c.Transport.Dial(handshakeCtx, address)
	if err != nil {
		return nil, err
	}
	if !transport.Verified(conn) {
		transport.Close(conn)
		return nil, transport.ErrTLSConfig
	}
	stop := context.AfterFunc(ctx, func() { transport.Close(conn) })
	keep := false
	defer func() {
		if !keep {
			stop()
			transport.Close(conn)
		}
	}()
	deadline, _ := handshakeCtx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	write := func(frame protocol.Frame, limit uint32) error {
		if err := conn.SetWriteDeadline(earlier(deadline, time.Now().Add(c.WriteTimeout))); err != nil {
			return err
		}
		return frame.EncodeWithLimit(conn, limit)
	}
	offer := protocol.Hello{Version: protocol.Version, Capabilities: []protocol.Capability{}, MaxPayloadSize: c.MaxFrame}
	frame, err := protocol.EncodeHello(offer)
	if err != nil {
		return nil, err
	}
	if err := write(frame, c.MaxFrame); err != nil {
		return nil, contextError(ctx, err)
	}
	reader := bufio.NewReader(conn)
	frame, err = protocol.DecodeTypes(reader, protocol.MaxHandshakePayloadSize, protocol.TypeHelloAck)
	if err != nil {
		return nil, contextError(ctx, err)
	}
	ack, err := protocol.DecodeHelloAck(frame)
	if err != nil {
		return nil, err
	}
	if err := ack.ValidateFor(offer); err != nil {
		return nil, err
	}
	if ack.MaxPayloadSize < protocol.MaxHandshakePayloadSize {
		return nil, protocol.ErrInvalidHandshake
	}
	frame, err = protocol.EncodeAuth(protocol.Auth{Token: token})
	if err != nil {
		return nil, err
	}
	if err := write(frame, ack.MaxPayloadSize); err != nil {
		return nil, contextError(ctx, err)
	}
	frame, err = protocol.DecodeTypes(reader, ack.MaxPayloadSize, protocol.TypeAuthOK, protocol.TypeAuthError)
	if err != nil {
		return nil, contextError(ctx, err)
	}
	if frame.Type == protocol.TypeAuthError {
		rejected, err := protocol.DecodeAuthError(frame)
		if err != nil {
			return nil, err
		}
		return nil, &AuthenticationError{Code: rejected.Code}
	}
	authenticated, err := protocol.DecodeAuthOK(frame)
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(authenticated.ExpiresAt) {
		return nil, &AuthenticationError{Code: protocol.AuthExpired}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keep = true
	return &Session{ConnectionID: authenticated.ConnectionID, ExpiresAt: authenticated.ExpiresAt, MaxPayloadSize: ack.MaxPayloadSize, conn: conn, reader: reader, ctx: ctx, idleTimeout: c.IdleTimeout, stop: stop}, nil
}

// Wait owns the session reader until closure. Closing the session or cancelling
// its parent context unblocks the read. Phase 2 accepts no subsequent messages.
func (s *Session) Wait() error {
	if !s.waiting.CompareAndSwap(false, true) {
		return ErrSessionInUse
	}
	defer s.Close()
	deadline := earlier(time.Now().Add(s.idleTimeout), s.ExpiresAt)
	if ctxDeadline, ok := s.ctx.Deadline(); ok {
		deadline = earlier(deadline, ctxDeadline)
	}
	if err := s.conn.SetReadDeadline(deadline); err != nil {
		return contextError(s.ctx, err)
	}
	_, err := protocol.DecodeTypes(s.reader, s.MaxPayloadSize)
	return contextError(s.ctx, err)
}

func (s *Session) Close() { s.closeOnce.Do(func() { s.stop(); transport.Close(s.conn) }) }
func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func contextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
