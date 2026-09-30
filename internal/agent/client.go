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
var ErrSessionInUse = errors.New("session operation is not valid in its current state")
var ErrRegistration = errors.New("relay registration failed")

type RegistrationError struct{ Code string }

func (e *RegistrationError) Error() string { return "relay registration failed: " + e.Code }
func (e *RegistrationError) Unwrap() error { return ErrRegistration }

type AuthenticationError struct{ Code string }

func (e *AuthenticationError) Error() string { return "relay authentication failed: " + e.Code }
func (e *AuthenticationError) Unwrap() error { return ErrAuthentication }

type Client struct {
	Transport           transport.Transport
	HandshakeTimeout    time.Duration
	RegistrationTimeout time.Duration
	IdleTimeout         time.Duration
	WriteTimeout        time.Duration
	MaxFrame            uint32
}

func NewClient(dialer transport.Transport) *Client {
	return &Client{Transport: dialer, HandshakeTimeout: 10 * time.Second, RegistrationTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, WriteTimeout: 5 * time.Second, MaxFrame: protocol.MaxPayloadSize}
}

type Session struct {
	ConnectionID        string
	ExpiresAt           time.Time
	MaxPayloadSize      uint32
	conn                net.Conn
	reader              *bufio.Reader
	ctx                 context.Context
	idleTimeout         time.Duration
	registrationTimeout time.Duration
	writeTimeout        time.Duration
	stop                func() bool
	closeOnce           sync.Once
	// 0 authenticated, 1 registering, 2 registered, 3 waiting, 4 closed.
	state atomic.Uint32
}

func (c *Client) Connect(ctx context.Context, address, token string) (*Session, error) {
	if c.Transport == nil || c.HandshakeTimeout <= 0 || c.RegistrationTimeout <= 0 || c.IdleTimeout <= 0 || c.WriteTimeout <= 0 || c.MaxFrame < protocol.MaxHandshakePayloadSize || c.MaxFrame > protocol.MaxPayloadSize || !protocol.ValidToken(token) {
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
	return &Session{ConnectionID: authenticated.ConnectionID, ExpiresAt: authenticated.ExpiresAt, MaxPayloadSize: ack.MaxPayloadSize, conn: conn, reader: reader, ctx: ctx, idleTimeout: c.IdleTimeout, registrationTimeout: c.RegistrationTimeout, writeTimeout: c.WriteTimeout, stop: stop}, nil
}

// Wait owns the session reader until closure. Closing the session or cancelling
// its parent context unblocks the read. Active traffic is a later phase.
func (s *Session) Wait() error {
	if !s.state.CompareAndSwap(0, 3) && !s.state.CompareAndSwap(2, 3) {
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

// Register serializes the request/ACK exchange against Wait and other calls.
// Any I/O/protocol failure closes the session; partial operations cannot retry.
func (s *Session) Register(ctx context.Context, request protocol.Register) (protocol.RegisterOK, error) {
	frame, err := protocol.EncodeRegister(request)
	if err != nil {
		return protocol.RegisterOK{}, err
	}
	if !s.state.CompareAndSwap(0, 1) {
		return protocol.RegisterOK{}, ErrSessionInUse
	}
	keep := false
	defer func() {
		if !keep {
			s.Close()
		}
	}()
	registrationCtx, cancel := context.WithTimeout(ctx, s.registrationTimeout)
	stop := context.AfterFunc(registrationCtx, func() { transport.Close(s.conn) })
	defer func() { stop(); cancel() }()
	deadline, _ := registrationCtx.Deadline()
	deadline = earlier(deadline, s.ExpiresAt)
	if parentDeadline, ok := s.ctx.Deadline(); ok {
		deadline = earlier(deadline, parentDeadline)
	}
	if err := s.conn.SetDeadline(deadline); err != nil {
		return protocol.RegisterOK{}, err
	}
	if err := s.conn.SetWriteDeadline(earlier(deadline, time.Now().Add(s.writeTimeout))); err != nil {
		return protocol.RegisterOK{}, err
	}
	if err := frame.EncodeWithLimit(s.conn, s.MaxPayloadSize); err != nil {
		return protocol.RegisterOK{}, contextError(s.ctx, contextError(registrationCtx, err))
	}
	frame, err = protocol.DecodeTypes(s.reader, s.MaxPayloadSize, protocol.TypeRegisterOK, protocol.TypeRegisterError)
	if err != nil {
		return protocol.RegisterOK{}, contextError(s.ctx, contextError(registrationCtx, err))
	}
	if frame.Type == protocol.TypeRegisterError {
		rejected, err := protocol.DecodeRegisterError(frame)
		if err != nil {
			return protocol.RegisterOK{}, err
		}
		return protocol.RegisterOK{}, &RegistrationError{Code: rejected.Code}
	}
	ack, err := protocol.DecodeRegisterOK(frame)
	if err != nil {
		return protocol.RegisterOK{}, err
	}
	if err := ack.ValidateFor(request, s.ConnectionID); err != nil {
		return protocol.RegisterOK{}, err
	}
	if err := s.ctx.Err(); err != nil {
		return protocol.RegisterOK{}, err
	}
	if err := registrationCtx.Err(); err != nil {
		return protocol.RegisterOK{}, err
	}
	if err := s.conn.SetDeadline(time.Time{}); err != nil {
		return protocol.RegisterOK{}, err
	}
	if !s.state.CompareAndSwap(1, 2) {
		return protocol.RegisterOK{}, net.ErrClosed
	}
	keep = true
	return ack, nil
}

func (s *Session) Close() {
	s.closeOnce.Do(func() { s.state.Store(4); s.stop(); transport.Close(s.conn) })
}
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
