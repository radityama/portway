package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
)

// Session is an immutable metadata snapshot, without access to its socket.
type Session struct {
	TunnelID       string
	ConnectionID   string
	Generation     protocol.Generation
	PublicHostname string
	ExpiresAt      time.Time
	LastSeen       time.Time
}

type registryEntry struct {
	Session
	conn             net.Conn
	ctx              context.Context
	streams          *mux.Conn
	connection       *mux.Conn
	registrationDone chan struct{}
}

func assignedHostname(tunnelID, base string) string {
	hash := sha256.Sum256([]byte(tunnelID))
	return "p-" + hex.EncodeToString(hash[:16]) + "." + base
}

// Lookup accepts a canonical hostname, never an HTTP Host header or URL. A
// public ingress owns normalization and port/SNI checks before lookup.
func (s *Server) Lookup(hostname string) (Session, bool) {
	if !protocol.ValidHostname(hostname) {
		return Session{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.hostnames[hostname]
	if !ok {
		return Session{}, false
	}
	owner := s.sessions[id]
	if owner == nil || owner.conn == nil || owner.ctx.Err() != nil || !time.Now().Before(owner.ExpiresAt) {
		return Session{}, false
	}
	return owner.Session, true
}

// register is reachable only after credential verification. All ownership
// changes and watermarks share one lock; socket closure happens outside it.
func (s *Server) register(ctx context.Context, identity auth.Identity, connectionID string, request protocol.Register, conn net.Conn) (*registryEntry, string) {
	if request.TunnelID != identity.TunnelID || identity.Generation != 0 && request.Generation != identity.Generation {
		return nil, protocol.RegisterForbidden
	}
	if ctx.Err() != nil || !time.Now().Before(identity.ExpiresAt) {
		return nil, protocol.RegisterForbidden
	}
	host := assignedHostname(request.TunnelID, s.PublicBaseDomain)
	owner := &registryEntry{Session: Session{TunnelID: request.TunnelID, ConnectionID: connectionID, Generation: request.Generation, PublicHostname: host, ExpiresAt: identity.ExpiresAt, LastSeen: time.Now()}, conn: conn, ctx: ctx}
	owner.registrationDone = make(chan struct{})
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return nil, protocol.RegisterDraining
	}
	previous := s.sessions[request.TunnelID]
	if previous != nil && previous.Generation >= request.Generation {
		s.mu.Unlock()
		return nil, protocol.RegisterStale
	}
	if previous == nil && len(s.sessions) >= s.MaxTunnels {
		s.mu.Unlock()
		return nil, protocol.RegisterCapacity
	}
	if id, exists := s.hostnames[host]; exists && id != request.TunnelID {
		s.mu.Unlock()
		return nil, protocol.RegisterConflict
	}
	var old net.Conn
	var oldStreams *mux.Conn
	if previous != nil {
		old = previous.conn
		oldStreams = previous.connection
		if oldStreams == nil {
			oldStreams = previous.streams
		}
	}
	s.sessions[request.TunnelID] = owner
	s.hostnames[host] = request.TunnelID
	s.mu.Unlock()
	if oldStreams != nil {
		oldStreams.Close()
	}
	if old != nil {
		_ = old.Close()
	}
	return owner, ""
}

func (s *Server) unregister(owner *registryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[owner.TunnelID] == owner {
		// Keep generation/hostname without retaining a live socket or context.
		s.sessions[owner.TunnelID] = &registryEntry{Session: owner.Session}
	}
}
