package relay

import (
	"context"
	"crypto/tls"
	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/control"
	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAliasesRequireCurrentGenerationAndLease(t *testing.T) {
	s := NewServer(nil)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	identity := auth.Identity{TunnelID: "tnl_alias", ExpiresAt: time.Now().Add(time.Minute)}
	owner, code := s.register(ctx, identity, "connection", protocol.Register{TunnelID: "tnl_alias", Generation: 1}, left)
	if code != "" {
		t.Fatal(code)
	}
	route := control.DomainRoute{Hostname: "app.example.test", TunnelID: "tnl_alias", Generation: 1, ExpiresAt: time.Now().Add(time.Minute)}
	if err := s.SetDomainRoutes([]control.DomainRoute{route}); err != nil {
		t.Fatal(err)
	}
	session, ok := s.Lookup(route.Hostname)
	if !ok || session.TunnelID != identity.TunnelID {
		t.Fatal("alias not resolved")
	}
	if err := s.SetDomainRoutes([]control.DomainRoute{route, route}); err == nil {
		t.Fatal("duplicate alias accepted")
	}
	if _, ok = s.Lookup(route.Hostname); !ok {
		t.Fatal("invalid update replaced good routes")
	}
	s.unregister(owner)
	second, code := s.register(ctx, identity, "new", protocol.Register{TunnelID: "tnl_alias", Generation: 2}, left)
	if code != "" {
		t.Fatal(code)
	}
	if _, ok = s.Lookup(route.Hostname); ok {
		t.Fatal("stale alias routed newer generation")
	}
	route.Generation = 2
	if err := s.SetDomainRoutes([]control.DomainRoute{route}); err != nil {
		t.Fatal(err)
	}
	if _, ok = s.Lookup(route.Hostname); !ok {
		t.Fatal("new generation not routed")
	}
	s.mu.Lock()
	expired := route
	expired.ExpiresAt = time.Now().Add(-time.Second)
	s.domains[route.Hostname] = expired
	s.mu.Unlock()
	if _, ok = s.Lookup(route.Hostname); ok || s.DomainAllowed(route.Hostname) {
		t.Fatal("expired snapshot retained routing or TLS policy")
	}
	if err := s.SetDomainRoutes([]control.DomainRoute{route}); err != nil {
		t.Fatal(err)
	}
	s.unregister(second)
	if _, ok = s.Lookup(route.Hostname); ok {
		t.Fatal("offline owner routed")
	}
	if err := s.SetDomainRoutes(nil); err != nil {
		t.Fatal(err)
	}
	if s.DomainAllowed(route.Hostname) {
		t.Fatal("removed alias authorized TLS")
	}
	route.Hostname = "p-stolen.portway.localhost"
	if s.SetDomainRoutes([]control.DomainRoute{route}) == nil {
		t.Fatal("reserved alias accepted")
	}
}

func TestCustomAliasReturns501ForLegacyAgent(t *testing.T) {
	s := NewServer(nil)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	expires := time.Now().Add(time.Minute)
	owner, code := s.register(ctx, auth.Identity{TunnelID: "tnl_legacy", ExpiresAt: expires}, "legacy", protocol.Register{TunnelID: "tnl_legacy", Generation: 1}, left)
	if code != "" {
		t.Fatal(code)
	}
	streams, err := mux.New(ctx, left, left, mux.Options{MaxStreams: 1, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	defer streams.Close()
	owner.streams = streams
	alias := "app.example.test"
	if err := s.SetDomainRoutes([]control.DomainRoute{{Hostname: alias, TunnelID: owner.TunnelID, Generation: owner.Generation, ExpiresAt: expires}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = alias
	req.TLS = &tls.ConnectionState{ServerName: alias}
	response := httptest.NewRecorder()
	s.ServeHTTP(response, req)
	if response.Code != 501 || streams.ActiveStreams() != 0 {
		t.Fatal("unnegotiated alias opened a stream")
	}
}
