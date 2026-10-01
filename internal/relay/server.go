package relay

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

var ErrConfig = errors.New("invalid relay TLS/authentication configuration")
var ErrCapacity = errors.New("relay connection limit reached")

type Server struct {
	Logger               *slog.Logger
	TLSConfig            *tls.Config
	Authenticator        auth.Verifier
	MaxConnections       int
	MaxPublicConnections int
	HandshakeTimeout     time.Duration
	RegistrationTimeout  time.Duration
	PublicBaseDomain     string
	PublicPort           int
	StreamTimeout        time.Duration
	MaxTunnels           int
	ReadIdleTimeout      time.Duration
	WriteTimeout         time.Duration
	MaxStreams           uint32
	MaxFrame             uint32
	mu                   sync.RWMutex
	sessions             map[string]*registryEntry
	hostnames            map[string]string
	active               map[net.Conn]struct{}
}

func NewServer(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{Logger: logger, MaxConnections: 128, MaxPublicConnections: 128, HandshakeTimeout: 10 * time.Second,
		RegistrationTimeout: 10 * time.Second, PublicBaseDomain: "portway.localhost", MaxTunnels: 1024,
		ReadIdleTimeout: 120 * time.Second, WriteTimeout: 5 * time.Second, MaxStreams: 32, StreamTimeout: 30 * time.Second,
		MaxFrame: protocol.MaxPayloadSize, sessions: make(map[string]*registryEntry), hostnames: make(map[string]string), active: make(map[net.Conn]struct{})}
}

func (s *Server) validate() error {
	if s.PublicPort < 0 || s.PublicPort > 65535 || s.MaxStreams < 1 || s.MaxStreams > 1024 || s.StreamTimeout <= 0 {
		return ErrConfig
	}
	if s.TLSConfig == nil || len(s.TLSConfig.Certificates) == 0 || s.TLSConfig.MinVersion < tls.VersionTLS13 || (s.TLSConfig.MaxVersion != 0 && s.TLSConfig.MaxVersion < tls.VersionTLS13) || s.Authenticator == nil || s.MaxConnections < 1 || s.MaxConnections > 10000 || s.MaxFrame < protocol.MaxHandshakePayloadSize || s.MaxFrame > protocol.MaxPayloadSize || s.HandshakeTimeout <= 0 || s.ReadIdleTimeout <= 0 || s.WriteTimeout <= 0 {
		return ErrConfig
	}
	if s.RegistrationTimeout <= 0 || s.MaxTunnels < 1 || s.MaxTunnels > 100000 || !protocol.ValidHostname(s.PublicBaseDomain) || len(s.PublicBaseDomain) > 218 {
		return ErrConfig
	}
	return nil
}

func (s *Server) admit(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active) >= s.MaxConnections {
		return false
	}
	s.active[conn] = struct{}{}
	return true
}

func (s *Server) release(conn net.Conn) {
	s.mu.Lock()
	delete(s.active, conn)
	s.mu.Unlock()
}

func (s *Server) ActiveConnections() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.active)
}

// Serve owns its listener and accepted connections. Cancellation closes all I/O
// and this call joins every admitted connection goroutine before returning.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	if err := s.validate(); err != nil {
		return err
	}
	lifeCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	stop := context.AfterFunc(lifeCtx, func() { _ = listener.Close() })
	defer stop()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if lifeCtx.Err() != nil {
				return nil
			}
			return errors.New("relay listener failed")
		}
		if !s.admit(conn) {
			_ = conn.Close()
			s.Logger.Warn("relay_connection_rejected", "reason", "capacity")
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := s.serveAdmitted(lifeCtx, conn)
			s.Logger.Debug("relay_connection_closed", "reason", closeReason(err))
		}()
	}
}

func (s *Server) ServeConn(ctx context.Context, conn net.Conn) error {
	if err := s.validate(); err != nil {
		_ = conn.Close()
		return err
	}
	if !s.admit(conn) {
		_ = conn.Close()
		return ErrCapacity
	}
	return s.serveAdmitted(ctx, conn)
}

func (s *Server) serveAdmitted(ctx context.Context, raw net.Conn) error {
	defer s.release(raw)
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	handshakeCtx, cancel := context.WithTimeout(ctx, s.HandshakeTimeout)
	defer cancel()
	deadline, _ := handshakeCtx.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		return err
	}
	config := s.TLSConfig.Clone()
	config.NextProtos = []string{transport.ALPN}
	config.SessionTicketsDisabled = true
	conn := tls.Server(raw, config)
	if err := conn.HandshakeContext(handshakeCtx); err != nil {
		return err
	}
	if conn.ConnectionState().NegotiatedProtocol != transport.ALPN {
		return transport.ErrTLSConfig
	}
	reader := bufio.NewReader(conn)
	frame, err := protocol.DecodeTypes(reader, protocol.MaxHandshakePayloadSize, protocol.TypeHello)
	if err != nil {
		return err
	}
	remote, err := protocol.DecodeHello(frame)
	if err != nil || remote.MaxPayloadSize < protocol.MaxHandshakePayloadSize {
		return protocol.ErrInvalidHandshake
	}
	ack, err := protocol.Negotiate(remote, protocol.Hello{Version: protocol.Version, Capabilities: []protocol.Capability{protocol.CapabilityMultiplexing, protocol.CapabilityFlowControl, protocol.CapabilityHeartbeat}, MaxPayloadSize: s.MaxFrame})
	if err != nil {
		return err
	}
	frame, err = protocol.EncodeHelloAck(ack)
	if err != nil {
		return err
	}
	if err := s.write(conn, frame, ack.MaxPayloadSize, deadline); err != nil {
		return err
	}
	frame, err = protocol.DecodeTypes(reader, ack.MaxPayloadSize, protocol.TypeAuth)
	if err != nil {
		return err
	}
	credential, err := protocol.DecodeAuth(frame)
	if err != nil {
		_ = s.rejectAuth(conn, protocol.AuthInvalid, ack.MaxPayloadSize, deadline)
		return auth.ErrInvalid
	}
	identity, err := s.Authenticator.Verify(handshakeCtx, credential.Token)
	credential.Token = ""
	if err != nil {
		code := protocol.AuthInvalid
		if errors.Is(err, auth.ErrExpired) {
			code = protocol.AuthExpired
		}
		if errors.Is(err, auth.ErrRevoked) {
			code = protocol.AuthRevoked
		}
		_ = s.rejectAuth(conn, code, ack.MaxPayloadSize, deadline)
		// A custom verifier may return sensitive error details. Keep only stable codes.
		return errors.New(code)
	}
	if !protocol.ValidTunnelID(identity.TunnelID) {
		_ = s.rejectAuth(conn, protocol.AuthInvalid, ack.MaxPayloadSize, deadline)
		return auth.ErrInvalid
	}
	if !time.Now().Before(identity.ExpiresAt) {
		_ = s.rejectAuth(conn, protocol.AuthExpired, ack.MaxPayloadSize, deadline)
		return auth.ErrExpired
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("connection identity generation failed")
	}
	connectionID := "con_" + hex.EncodeToString(random[:])
	frame, err = protocol.EncodeAuthOK(protocol.AuthOK{ConnectionID: connectionID, ExpiresAt: identity.ExpiresAt})
	if err != nil {
		return err
	}
	if err := s.write(conn, frame, ack.MaxPayloadSize, earlier(deadline, identity.ExpiresAt)); err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	s.Logger.Info("relay_authenticated", "connection_id", connectionID, "expires_at", identity.ExpiresAt)
	defer s.Logger.Info("relay_disconnected", "connection_id", connectionID)
	registrationDeadline := earlier(time.Now().Add(s.RegistrationTimeout), identity.ExpiresAt)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		registrationDeadline = earlier(registrationDeadline, ctxDeadline)
	}
	if err := conn.SetReadDeadline(registrationDeadline); err != nil {
		return err
	}
	frame, err = protocol.DecodeTypes(reader, ack.MaxPayloadSize, protocol.TypeRegister)
	if err != nil {
		return err
	}
	request, err := protocol.DecodeRegister(frame)
	if err != nil {
		_ = s.rejectRegistration(conn, protocol.RegisterInvalid, ack.MaxPayloadSize, registrationDeadline)
		return protocol.ErrInvalidHandshake
	}
	if request.Protocol == "http" && (s.PublicPort == 0 || !slices.Contains(ack.Capabilities, protocol.CapabilityMultiplexing) || !slices.Contains(ack.Capabilities, protocol.CapabilityFlowControl)) {
		_ = s.rejectRegistration(conn, protocol.RegisterInvalid, ack.MaxPayloadSize, registrationDeadline)
		return protocol.ErrInvalidHandshake
	}
	var streams *mux.Conn
	heartbeat := slices.Contains(ack.Capabilities, protocol.CapabilityHeartbeat)
	httpMode := request.Protocol == "http"
	if httpMode || heartbeat {
		streams, err = mux.New(ctx, conn, reader, mux.Options{MaxStreams: int(s.MaxStreams), MaxFrame: ack.MaxPayloadSize, StreamTimeout: s.StreamTimeout, WriteTimeout: s.WriteTimeout, IdleTimeout: s.ReadIdleTimeout, ExpiresAt: identity.ExpiresAt, Diagnostic: !httpMode, Heartbeat: heartbeat})
		if err != nil {
			return err
		}
		defer streams.Close()
	}
	owner, code := s.register(ctx, identity, connectionID, request, raw)
	if code != "" {
		_ = s.rejectRegistration(conn, code, ack.MaxPayloadSize, registrationDeadline)
		return errors.New(code)
	}
	defer s.unregister(owner)
	var finishRegistration func()
	if httpMode {
		finishRegistration = sync.OnceFunc(func() { close(owner.registrationDone) })
		defer finishRegistration()
	}
	publicURL := ""
	if httpMode {
		publicURL = "https://" + owner.PublicHostname
		if s.PublicPort != 443 {
			publicURL += ":" + strconv.Itoa(s.PublicPort)
		}
	}
	frame, err = protocol.EncodeRegisterOK(protocol.RegisterOK{TunnelID: owner.TunnelID, ConnectionID: owner.ConnectionID, Generation: owner.Generation, PublicHostname: owner.PublicHostname, PublicURL: publicURL})
	if err != nil {
		return err
	}
	if err := s.write(conn, frame, ack.MaxPayloadSize, registrationDeadline); err != nil {
		return err
	}
	s.Logger.Info("relay_registered", "tunnel_id", owner.TunnelID, "connection_id", owner.ConnectionID, "generation", owner.Generation.String(), "hostname", owner.PublicHostname)
	if streams != nil {
		if httpMode {
			s.mu.Lock()
			if s.sessions[owner.TunnelID] == owner {
				owner.streams = streams
			}
			s.mu.Unlock()
			finishRegistration()
		}
		return streams.Run()
	}
	readDeadline := earlier(time.Now().Add(s.ReadIdleTimeout), identity.ExpiresAt)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		readDeadline = earlier(readDeadline, ctxDeadline)
	}
	if err := conn.SetReadDeadline(readDeadline); err != nil {
		return err
	}
	// Diagnostic registrations do not serve streams. Reject unexpected headers
	// before allocating/reading a body, including a second REGISTER.
	_, err = protocol.DecodeTypes(reader, ack.MaxPayloadSize)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (s *Server) write(conn net.Conn, frame protocol.Frame, limit uint32, deadline time.Time) error {
	if err := conn.SetWriteDeadline(earlier(deadline, time.Now().Add(s.WriteTimeout))); err != nil {
		return err
	}
	return frame.EncodeWithLimit(conn, limit)
}

func (s *Server) rejectAuth(conn net.Conn, code string, limit uint32, deadline time.Time) error {
	s.Logger.Warn("relay_authentication_rejected", "code", code)
	frame, err := protocol.EncodeAuthError(protocol.AuthError{Code: code})
	if err != nil {
		return err
	}
	return s.write(conn, frame, limit, deadline)
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func closeReason(err error) string {
	if errors.Is(err, mux.ErrHeartbeatTimeout) {
		return "heartbeat_timeout"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	if errors.Is(err, io.EOF) {
		return "peer_closed"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	return "connection_rejected"
}

func (s *Server) rejectRegistration(conn net.Conn, code string, limit uint32, deadline time.Time) error {
	s.Logger.Warn("relay_registration_rejected", "code", code)
	frame, err := protocol.EncodeRegisterError(protocol.RegisterError{Code: code})
	if err != nil {
		return err
	}
	return s.write(conn, frame, limit, deadline)
}
