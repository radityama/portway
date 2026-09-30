package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

type Server struct {
	Logger          *slog.Logger
	MaxStreams      uint32
	MaxFrame        uint32
	ReadIdleTimeout time.Duration

	mu       sync.RWMutex
	sessions map[string]*Session
}

type Session struct {
	TunnelID     string
	ConnectionID string
	Generation   uint64
	Conn         net.Conn
	LastSeen     time.Time
}

func NewServer(logger *slog.Logger) *Server {
	return &Server{Logger: logger, MaxStreams: 1024, MaxFrame: protocol.MaxPayloadSize, ReadIdleTimeout: 45 * time.Second, sessions: make(map[string]*Session)}
}

func (s *Server) Register(ctx context.Context, session *Session) error {
	if session.TunnelID == "" || session.ConnectionID == "" {
		return errors.New("missing tunnel or connection id")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.sessions[session.TunnelID]; ok && previous.Generation > session.Generation {
		return fmt.Errorf("stale generation %d; current generation is %d", session.Generation, previous.Generation)
	}
	s.sessions[session.TunnelID] = session
	return nil
}

func (s *Server) ServeConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		deadline := time.Now().Add(s.ReadIdleTimeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		_ = conn.SetReadDeadline(deadline)
		frame, err := protocol.DecodeWithLimit(reader, s.MaxFrame)
		if err != nil {
			return err
		}
		if frame.Type == protocol.TypePing {
			if err := (protocol.Frame{Type: protocol.TypePong, StreamID: frame.StreamID}).Encode(conn); err != nil {
				return err
			}
		}
	}
}
