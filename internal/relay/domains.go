package relay

import (
	"github.com/radityama/portway/internal/control"
	"github.com/radityama/portway/internal/protocol"
	"strings"
	"time"
)

func (s *Server) SetDomainRoutes(routes []control.DomainRoute) error {
	if len(routes) > 128 {
		return ErrConfig
	}
	next := make(map[string]control.DomainRoute, len(routes))
	now := time.Now()
	for _, r := range routes {
		if !protocol.ValidHostname(r.Hostname) || len(r.Hostname) > 220 || !strings.Contains(r.Hostname, ".") || r.Hostname == s.PublicBaseDomain || strings.HasSuffix(r.Hostname, "."+s.PublicBaseDomain) || !protocol.ValidTunnelID(r.TunnelID) || r.Generation == 0 || !r.ExpiresAt.After(now) || r.ExpiresAt.After(now.Add(15*time.Minute+30*time.Second)) {
			return ErrConfig
		}
		if _, duplicate := next[r.Hostname]; duplicate {
			return ErrConfig
		}
		next[r.Hostname] = r
	}
	s.mu.Lock()
	s.domains = next
	s.mu.Unlock()
	return nil
}
func (s *Server) DomainAllowed(host string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.domains[host]
	return ok && time.Now().Before(r.ExpiresAt)
}

// resolveLocked also binds aliases to the exact currently admitted generation.
func (s *Server) resolveLocked(host string) (string, bool) {
	if id, ok := s.hostnames[host]; ok {
		return id, true
	}
	r, ok := s.domains[host]
	if !ok || !time.Now().Before(r.ExpiresAt) {
		return "", false
	}
	owner := s.sessions[r.TunnelID]
	if owner != nil && owner.Generation != r.Generation {
		return "", false
	}
	return r.TunnelID, true
}
