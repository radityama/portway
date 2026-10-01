package relay

import (
	"context"
	"crypto/tls"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/protocol"
)

func TestRegistryOwnershipCleanupAndCapacity(t *testing.T) {
	s := NewServer(nil)
	s.MaxTunnels = 1
	identity := auth.Identity{TunnelID: "tnl_fixture", ExpiresAt: time.Now().Add(time.Hour)}
	first, peer1 := net.Pipe()
	defer first.Close()
	defer peer1.Close()
	one, code := s.register(context.Background(), identity, "first", protocol.Register{TunnelID: identity.TunnelID, Generation: 1}, first)
	if code != "" {
		t.Fatal(code)
	}
	second, peer2 := net.Pipe()
	defer second.Close()
	defer peer2.Close()
	two, code := s.register(context.Background(), identity, "second", protocol.Register{TunnelID: identity.TunnelID, Generation: 2}, second)
	if code != "" {
		t.Fatal(code)
	}
	_ = peer1.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer1.Read(make([]byte, 1)); err == nil {
		t.Fatal("old owner socket not closed")
	}
	s.unregister(one)
	got, ok := s.Lookup(two.PublicHostname)
	if !ok || got.ConnectionID != "second" {
		t.Fatal("old cleanup removed new route")
	}
	got.ConnectionID = "mutated"
	if got, _ := s.Lookup(two.PublicHostname); got.ConnectionID != "second" {
		t.Fatal("lookup exposed mutable state")
	}
	s.unregister(two)
	if _, ok := s.Lookup(two.PublicHostname); ok {
		t.Fatal("offline owner still routable")
	}
	if _, code := s.register(context.Background(), identity, "stale", protocol.Register{TunnelID: identity.TunnelID, Generation: 2}, second); code != protocol.RegisterStale {
		t.Fatal("disconnect lost watermark")
	}
	other := auth.Identity{TunnelID: "tnl_other", ExpiresAt: identity.ExpiresAt}
	if _, code := s.register(context.Background(), other, "other", protocol.Register{TunnelID: other.TunnelID, Generation: 1}, second); code != protocol.RegisterCapacity {
		t.Fatal("watermark capacity unbounded")
	}
	if _, code := s.register(context.Background(), identity, "third", protocol.Register{TunnelID: identity.TunnelID, Generation: 3}, second); code != "" {
		t.Fatal("capacity blocked existing tunnel replacement")
	}
	if len(s.sessions) != 1 || len(s.hostnames) != 1 {
		t.Fatal("registry grew past bound")
	}
}

func TestRegistryConcurrentGenerations(t *testing.T) {
	s := NewServer(nil)
	identity := auth.Identity{TunnelID: "tnl_fixture", ExpiresAt: time.Now().Add(time.Hour)}
	var workers sync.WaitGroup
	for n := protocol.Generation(1); n <= 32; n++ {
		conn, peer := net.Pipe()
		defer conn.Close()
		defer peer.Close()
		workers.Add(1)
		go func(g protocol.Generation, conn net.Conn) {
			defer workers.Done()
			_, code := s.register(context.Background(), identity, "connection", protocol.Register{TunnelID: identity.TunnelID, Generation: g}, conn)
			if code != "" && code != protocol.RegisterStale {
				t.Error(code)
			}
		}(n, conn)
	}
	workers.Wait()
	got, ok := s.Lookup(assignedHostname(identity.TunnelID, s.PublicBaseDomain))
	if !ok || got.Generation != 32 {
		t.Fatal("concurrent registration lost newest generation")
	}
}

func TestRegistryExpiryConflictAndHostValidation(t *testing.T) {
	s := NewServer(nil)
	identity := auth.Identity{TunnelID: "tnl_fixture", ExpiresAt: time.Now().Add(time.Hour)}
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	request := protocol.Register{TunnelID: identity.TunnelID, Generation: 1}
	ctx, cancel := context.WithCancel(context.Background())
	owner, code := s.register(ctx, identity, "connection", request, conn)
	if code != "" {
		t.Fatal(code)
	}
	cancel()
	if _, ok := s.Lookup(owner.PublicHostname); ok {
		t.Fatal("cancelled context remained routable")
	}
	s.unregister(owner)
	for _, host := range []string{owner.PublicHostname + ":443", owner.PublicHostname + ".", "https://" + owner.PublicHostname, "unknown.example.com"} {
		if _, ok := s.Lookup(host); ok {
			t.Fatal("unsafe/unassigned host resolved")
		}
	}
	identity.ExpiresAt = time.Now().Add(-time.Second)
	if _, code := s.register(context.Background(), identity, "expired", request, conn); code != protocol.RegisterForbidden {
		t.Fatal("expired identity registered")
	}
	identity.ExpiresAt = time.Now().Add(time.Hour)
	s = NewServer(nil)
	s.hostnames[assignedHostname(identity.TunnelID, s.PublicBaseDomain)] = "tnl_collision"
	if _, code := s.register(context.Background(), identity, "collision", request, conn); code != protocol.RegisterConflict {
		t.Fatal("hostname collision overwritten")
	}
}

func TestHTTPPendingRegistrationWaitIsBoundedAndCancelable(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "failed_ack"} {
		t.Run(mode, func(t *testing.T) {
			server := NewServer(nil)
			server.PublicPort = 8443
			server.WriteTimeout = 20 * time.Millisecond
			identity := auth.Identity{TunnelID: "tnl_fixture", ExpiresAt: time.Now().Add(time.Minute)}
			conn, peer := net.Pipe()
			defer conn.Close()
			defer peer.Close()
			owner, code := server.register(context.Background(), identity, "connection", protocol.Register{TunnelID: identity.TunnelID, Generation: 1, Protocol: "http"}, conn)
			if code != "" {
				t.Fatal(code)
			}
			request := httptest.NewRequest("GET", "/", nil)
			request.Host = owner.PublicHostname + ":8443"
			request.TLS = &tls.ConnectionState{ServerName: owner.PublicHostname}
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			request = request.WithContext(ctx)
			if mode == "failed_ack" {
				close(owner.registrationDone)
				server.unregister(owner)
			}
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { server.ServeHTTP(response, request); close(done) }()
			if mode == "cancel" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("pending ACK retained public request")
			}
			if mode != "cancel" && response.Code != 503 {
				t.Fatal("incomplete/failed ACK enabled forwarding")
			}
		})
	}
}
