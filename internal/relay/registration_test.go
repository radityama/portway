package relay_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/radityama/portway/internal/agent"
	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/relay"
	"github.com/radityama/portway/internal/transport"
)

func connected(t *testing.T, f *fixture) *agent.Session {
	t.Helper()
	session, err := f.client.Connect(context.Background(), f.address, f.token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	return session
}
func registered(t *testing.T, f *fixture, generation protocol.Generation) (*agent.Session, protocol.RegisterOK) {
	t.Helper()
	session := connected(t, f)
	ack, err := session.Register(context.Background(), protocol.Register{TunnelID: "tnl_local_dev", Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	return session, ack
}
func authenticatedRaw(t *testing.T, f *fixture) net.Conn {
	t.Helper()
	conn := rawTLS(t, f)
	hello(t, conn)
	frame, err := protocol.EncodeAuth(protocol.Auth{Token: f.token})
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	frame, err = protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.DecodeAuthOK(frame); err != nil {
		t.Fatal(err)
	}
	return conn
}
func registrationCode(t *testing.T, session *agent.Session, id string, generation protocol.Generation, want string) {
	t.Helper()
	_, err := session.Register(context.Background(), protocol.Register{TunnelID: id, Generation: generation})
	var rejected *agent.RegistrationError
	if !errors.As(err, &rejected) || rejected.Code != want {
		t.Fatalf("expected %s, got %v", want, err)
	}
}

func TestRegisteredHostnameReplacementAndStaleRejection(t *testing.T) {
	f := setup(t, nil)
	first, ack1 := registered(t, f, 10)
	route, ok := f.server.Lookup(ack1.PublicHostname)
	if !ok || route.ConnectionID != first.ConnectionID || route.Generation != 10 {
		t.Fatal("hostname did not resolve authenticated owner")
	}
	registrationCode(t, connected(t, f), "tnl_other", 11, protocol.RegisterForbidden)
	registrationCode(t, connected(t, f), "tnl_local_dev", 9, protocol.RegisterStale)
	registrationCode(t, connected(t, f), "tnl_local_dev", 10, protocol.RegisterStale)
	second, ack2 := registered(t, f, 11)
	if ack2.PublicHostname != ack1.PublicHostname {
		t.Fatal("hostname changed across generations")
	}
	done := make(chan error, 1)
	go func() { done <- first.Wait() }()
	if err := await(t, done); err == nil {
		t.Fatal("superseded agent stayed connected")
	}
	eventually(t, func() bool { return f.server.ActiveConnections() == 1 })
	route, ok = f.server.Lookup(ack2.PublicHostname)
	if !ok || route.ConnectionID != second.ConnectionID {
		t.Fatal("stale cleanup removed newest route")
	}
	second.Close()
	eventually(t, func() bool { _, ok := f.server.Lookup(ack2.PublicHostname); return !ok })
	registrationCode(t, connected(t, f), "tnl_local_dev", 10, protocol.RegisterStale)
	registrationCode(t, connected(t, f), "tnl_local_dev", 11, protocol.RegisterStale)
	_, ack3 := registered(t, f, 12)
	if ack3.PublicHostname != ack1.PublicHostname {
		t.Fatal("reconnect lost hostname")
	}
}

func TestRegistrationCapacityRetainsOfflineWatermarks(t *testing.T) {
	otherToken := "fixture_other_credential_only_for_tests"
	f := setup(t, func(server *relay.Server, records []auth.Record) {
		server.MaxTunnels = 1
		hash := sha256.Sum256([]byte(otherToken))
		other := records[0]
		other.TunnelID = "tnl_other"
		other.TokenHash = hex.EncodeToString(hash[:])
		var err error
		server.Authenticator, err = auth.NewVerifier(append(records, other))
		if err != nil {
			t.Fatal(err)
		}
	})
	first, _ := registered(t, f, 1)
	first.Close()
	eventually(t, func() bool { return f.server.ActiveConnections() == 0 })
	session, err := f.client.Connect(context.Background(), f.address, otherToken)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	registrationCode(t, session, "tnl_other", 1, protocol.RegisterCapacity)
	_, _ = registered(t, f, 2)
}

func TestRegistrationTimeoutMalformedAndWrongSequence(t *testing.T) {
	for _, kind := range []string{"stall", "partial body", "malformed", "oversized", "before AUTH", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, func(server *relay.Server, _ []auth.Record) { server.RegistrationTimeout = 100 * time.Millisecond })
			var conn net.Conn
			if kind == "before AUTH" {
				conn = rawTLS(t, f)
				hello(t, conn)
			} else {
				conn = authenticatedRaw(t, f)
			}
			switch kind {
			case "partial body", "oversized":
				header := make([]byte, 16)
				header[0] = 1
				header[1] = byte(protocol.TypeRegister)
				length := uint32(1)
				if kind == "oversized" {
					length = 4097
				}
				binary.BigEndian.PutUint32(header[12:], length)
				if _, err := conn.Write(header); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				frame := protocol.Frame{Type: protocol.TypeRegister, Payload: []byte(`{"tunnel_id":"tnl_local_dev","generation":1}`)}
				if err := frame.Encode(conn); err != nil {
					t.Fatal(err)
				}
				response, err := protocol.Decode(conn)
				if err != nil {
					t.Fatal(err)
				}
				value, err := protocol.DecodeRegisterError(response)
				if err != nil || value.Code != protocol.RegisterInvalid {
					t.Fatal("malformed request not rejected")
				}
			case "before AUTH", "duplicate":
				frame, err := protocol.EncodeRegister(protocol.Register{TunnelID: "tnl_local_dev", Generation: 1})
				if err != nil {
					t.Fatal(err)
				}
				if err := frame.Encode(conn); err != nil {
					t.Fatal(err)
				}
				if kind == "duplicate" {
					response, err := protocol.Decode(conn)
					if err != nil {
						t.Fatal(err)
					}
					ack, err := protocol.DecodeRegisterOK(response)
					if err != nil {
						t.Fatal(err)
					}
					// No body follows this duplicate header; reject by state immediately.
					header := make([]byte, 16)
					header[0] = 1
					header[1] = byte(protocol.TypeRegister)
					binary.BigEndian.PutUint32(header[12:], 1)
					if _, err := conn.Write(header); err != nil {
						t.Fatal(err)
					}
					eventually(t, func() bool { _, ok := f.server.Lookup(ack.PublicHostname); return !ok })
				}
			}
			expectClosed(t, conn)
			eventually(t, func() bool { return f.server.ActiveConnections() == 0 })
		})
	}
}

func TestRegisteredCancellationExpiryAndShutdownCleanRoutes(t *testing.T) {
	for _, kind := range []string{"cancel", "expiry", "shutdown", "idle"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, func(server *relay.Server, records []auth.Record) {
				if kind == "expiry" {
					records[0].ExpiresAt = time.Now().Add(400 * time.Millisecond)
				}
				if kind == "idle" {
					server.ReadIdleTimeout = 100 * time.Millisecond
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session, err := f.client.Connect(ctx, f.address, f.token)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			ack, err := session.Register(ctx, protocol.Register{TunnelID: "tnl_local_dev", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- session.Wait() }()
			if kind == "cancel" {
				cancel()
			}
			if kind == "shutdown" {
				f.stop()
			}
			if err := await(t, done); err == nil {
				t.Fatal("connection did not close")
			}
			eventually(t, func() bool {
				_, ok := f.server.Lookup(ack.PublicHostname)
				return !ok && f.server.ActiveConnections() == 0
			})
		})
	}
}

func TestSlowRegistrationACKWriteClosesAndRetainsGeneration(t *testing.T) {
	f := setup(t, nil)
	f.stop()
	f.server.WriteTimeout = 50 * time.Millisecond
	serverRaw, clientRaw := net.Pipe()
	defer serverRaw.Close()
	defer clientRaw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.server.ServeConn(ctx, serverRaw) }()
	conn := tls.Client(clientRaw, f.tlsConfig)
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	hello(t, conn)
	frame, _ := protocol.EncodeAuth(protocol.Auth{Token: f.token})
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.Decode(conn); err != nil {
		t.Fatal(err)
	}
	frame, _ = protocol.EncodeRegister(protocol.Register{TunnelID: "tnl_local_dev", Generation: 9})
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	var timeout net.Error
	if err := await(t, done); !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("ACK write lacked timeout: %v", err)
	}
	// A new connection cannot reuse a generation whose ACK was lost.
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	go func() { done <- f.server.ServeConn(ctx, srv) }()
	conn = tls.Client(cli, f.tlsConfig)
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	hello(t, conn)
	frame, _ = protocol.EncodeAuth(protocol.Auth{Token: f.token})
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.Decode(conn); err != nil {
		t.Fatal(err)
	}
	frame, _ = protocol.EncodeRegister(protocol.Register{TunnelID: "tnl_local_dev", Generation: 9})
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := protocol.DecodeRegisterError(response)
	if err != nil || rejected.Code != protocol.RegisterStale {
		t.Fatal("failed ACK lost watermark")
	}
	_ = await(t, done)
	transport.Close(conn)
}
