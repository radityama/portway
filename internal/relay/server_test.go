package relay_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/radityama/portway/internal/agent"
	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/devsetup"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/relay"
	"github.com/radityama/portway/internal/transport"
)

type fixture struct {
	server    *relay.Server
	client    *agent.Client
	address   string
	token     string
	tlsConfig *tls.Config
	stop      func()
}

func setup(t *testing.T, configure func(*relay.Server, []auth.Record)) *fixture {
	t.Helper()
	dir := t.TempDir()
	if err := devsetup.Ensure(dir, false); err != nil {
		t.Fatal(err)
	}
	token, err := auth.ReadTokenFile(filepath.Join(dir, "agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "relay-credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []auth.Record
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	server := relay.NewServer(slog.New(slog.NewJSONHandler(&logs, nil)))
	server.TLSConfig, err = transport.ServerConfig(filepath.Join(dir, "relay-cert.pem"), filepath.Join(dir, "relay-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if configure != nil {
		configure(server, records)
	}
	if server.Authenticator == nil {
		server.Authenticator, err = auth.NewVerifier(records)
		if err != nil {
			t.Fatal(err)
		}
	}
	clientConfig, err := transport.ClientConfig(filepath.Join(dir, "ca.pem"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			if err := await(t, done); err != nil {
				t.Errorf("shutdown: %v", err)
			}
			if server.ActiveConnections() != 0 {
				t.Error("shutdown retained connections")
			}
			if strings.Contains(logs.String(), token) {
				t.Error("credential appeared in relay logs")
			}
		})
	}
	t.Cleanup(stop)
	client := agent.NewClient(&transport.TLSDialer{Config: clientConfig, Timeout: time.Second})
	client.HandshakeTimeout = time.Second
	return &fixture{server: server, client: client, address: listener.Addr().String(), token: token, tlsConfig: clientConfig, stop: stop}
}

func await(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("connection goroutine did not stop")
		return nil
	}
}
func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("connection state did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func rawTLS(t *testing.T, f *fixture) net.Conn {
	t.Helper()
	conn, err := (&transport.TLSDialer{Config: f.tlsConfig, Timeout: time.Second}).Dial(context.Background(), f.address)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { transport.Close(conn) })
	return conn
}
func hello(t *testing.T, conn net.Conn) protocol.HelloAck {
	t.Helper()
	frame, err := protocol.EncodeHello(protocol.Hello{Version: 1, Capabilities: []protocol.Capability{}, MaxPayloadSize: protocol.MaxPayloadSize})
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := protocol.DecodeHelloAck(response)
	if err != nil {
		t.Fatal(err)
	}
	return ack
}
func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("peer was not closed")
	} else {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatal("peer remained open until the test deadline")
		}
	}
}

func TestAuthenticatedTLSAndFreshReconnect(t *testing.T) {
	f := setup(t, func(server *relay.Server, _ []auth.Record) { server.MaxFrame = 65536 })
	var previous string
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		session, err := f.client.Connect(ctx, f.address, f.token)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if !strings.HasPrefix(session.ConnectionID, "con_") || session.ConnectionID == previous || session.MaxPayloadSize != 65536 {
			t.Fatal("invalid authenticated connection metadata")
		}
		previous = session.ConnectionID
		session.Close()
		cancel()
		eventually(t, func() bool { return f.server.ActiveConnections() == 0 })
	}
}

func TestInvalidExpiredAndRevokedCredentials(t *testing.T) {
	for _, code := range []string{protocol.AuthInvalid, protocol.AuthExpired, protocol.AuthRevoked} {
		t.Run(code, func(t *testing.T) {
			f := setup(t, func(_ *relay.Server, records []auth.Record) {
				if code == protocol.AuthExpired {
					records[0].ExpiresAt = time.Now().Add(-time.Minute)
				}
				if code == protocol.AuthRevoked {
					now := time.Now()
					records[0].RevokedAt = &now
				}
			})
			token := f.token
			if code == protocol.AuthInvalid {
				token = strings.Repeat("x", 64)
			}
			session, err := f.client.Connect(context.Background(), f.address, token)
			var rejected *agent.AuthenticationError
			if session != nil || !errors.As(err, &rejected) || rejected.Code != code {
				t.Fatalf("expected %s authentication failure: %v", code, err)
			}
			eventually(t, func() bool { return f.server.ActiveConnections() == 0 })
		})
	}
}

func TestUntrustedCertificateAndHostname(t *testing.T) {
	f := setup(t, nil)
	other := t.TempDir()
	if err := devsetup.Ensure(other, false); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"CA", "hostname", "insecure"} {
		t.Run(kind, func(t *testing.T) {
			cfg := f.tlsConfig.Clone()
			if kind == "CA" {
				var err error
				cfg, err = transport.ClientConfig(filepath.Join(other, "ca.pem"), "localhost")
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "hostname" {
				cfg.ServerName = "wrong.invalid"
			}
			if kind == "insecure" {
				cfg.InsecureSkipVerify = true
			}
			client := agent.NewClient(&transport.TLSDialer{Config: cfg, Timeout: time.Second})
			if session, err := client.Connect(context.Background(), f.address, f.token); err == nil || session != nil {
				t.Fatal("TLS verification failure authenticated")
			}
		})
	}
}

func TestALPNAndTLSVersionAreRequired(t *testing.T) {
	f := setup(t, nil)
	for _, kind := range []string{"missing ALPN", "wrong ALPN", "TLS 1.2"} {
		t.Run(kind, func(t *testing.T) {
			cfg := f.tlsConfig.Clone()
			cfg.NextProtos = nil
			if kind == "wrong ALPN" {
				cfg.NextProtos = []string{"wrong/1"}
			}
			if kind == "TLS 1.2" {
				cfg.MinVersion = tls.VersionTLS12
				cfg.MaxVersion = tls.VersionTLS12
				cfg.NextProtos = []string{transport.ALPN}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := (&tls.Dialer{Config: cfg}).DialContext(ctx, "tcp", f.address)
			if err == nil {
				defer transport.Close(conn)
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				expectClosed(t, conn)
			}
		})
	}
}

func TestStateSequenceRejectsBodiesBeforeReading(t *testing.T) {
	f := setup(t, nil)
	for _, stage := range []string{"before HELLO", "before AUTH", "after AUTH"} {
		t.Run(stage, func(t *testing.T) {
			conn := rawTLS(t, f)
			if stage != "before HELLO" {
				hello(t, conn)
			}
			if stage == "after AUTH" {
				frame, err := protocol.EncodeAuth(protocol.Auth{Token: f.token})
				if err != nil {
					t.Fatal(err)
				}
				if err := frame.Encode(conn); err != nil {
					t.Fatal(err)
				}
				response, err := protocol.Decode(conn)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := protocol.DecodeAuthOK(response); err != nil {
					t.Fatal(err)
				}
			}
			// Promise a payload but send only the header. A state check must not wait
			// for the body. DATA is invalid in every Phase 2 session state.
			header := make([]byte, 16)
			header[0] = 1
			header[1] = 0x13
			binary.BigEndian.PutUint64(header[4:12], 1)
			binary.BigEndian.PutUint32(header[12:], 1)
			if _, err := conn.Write(header); err != nil {
				t.Fatal(err)
			}
			expectClosed(t, conn)
		})
	}
}

func TestVersionAndMalformedAuthRejection(t *testing.T) {
	f := setup(t, nil)
	conn := rawTLS(t, f)
	header := make([]byte, 16)
	header[0] = 2
	header[1] = 1
	if _, err := conn.Write(header); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, conn)
	conn = rawTLS(t, f)
	hello(t, conn)
	frame := protocol.Frame{Type: protocol.TypeAuth, Payload: []byte(`{"token":null}`)}
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := protocol.DecodeAuthError(response)
	if err != nil || rejected.Code != protocol.AuthInvalid {
		t.Fatal("malformed AUTH did not fail safely")
	}
	expectClosed(t, conn)
}

func TestConfiguredFrameLimitIsEnforced(t *testing.T) {
	f := setup(t, func(server *relay.Server, _ []auth.Record) { server.MaxFrame = 4096 })
	conn := rawTLS(t, f)
	hello(t, conn)
	frame, err := protocol.EncodeAuth(protocol.Auth{Token: f.token})
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.Decode(conn); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 16)
	header[0] = 1
	header[1] = 0x13
	binary.BigEndian.PutUint64(header[4:12], 1)
	binary.BigEndian.PutUint32(header[12:], 4097)
	if _, err := conn.Write(header); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, conn)
}

func TestSlowPeersAndPlaintextAreClosed(t *testing.T) {
	for _, kind := range []string{"TLS stall", "HELLO stall", "plaintext"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, func(server *relay.Server, _ []auth.Record) { server.HandshakeTimeout = 250 * time.Millisecond })
			var conn net.Conn
			var err error
			if kind == "HELLO stall" {
				conn = rawTLS(t, f)
			} else {
				conn, err = net.DialTimeout("tcp", f.address, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				if kind == "plaintext" {
					_, _ = conn.Write([]byte("not a TLS connection"))
				}
			}
			expectClosed(t, conn)
		})
	}
}

func TestConnectionCapacityAndShutdown(t *testing.T) {
	f := setup(t, func(server *relay.Server, _ []auth.Record) { server.MaxConnections = 1 })
	first, err := net.DialTimeout("tcp", f.address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	eventually(t, func() bool { return f.server.ActiveConnections() == 1 })
	second, err := net.DialTimeout("tcp", f.address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetDeadline(time.Now().Add(time.Second))
	expectClosed(t, second)
	f.stop()
	_ = first.SetDeadline(time.Now().Add(time.Second))
	expectClosed(t, first)
}

func TestAgentCancellationAndRelayShutdown(t *testing.T) {
	for _, kind := range []string{"agent cancellation", "relay shutdown", "credential expiry"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, func(_ *relay.Server, records []auth.Record) {
				if kind == "credential expiry" {
					records[0].ExpiresAt = time.Now().Add(400 * time.Millisecond)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session, err := f.client.Connect(ctx, f.address, f.token)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- session.Wait() }()
			if kind == "agent cancellation" {
				cancel()
			}
			if kind == "relay shutdown" {
				f.stop()
			}
			if err := await(t, done); err == nil {
				t.Fatal("session did not report closure")
			} else if kind == "agent cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was not preserved: %v", err)
			}
			eventually(t, func() bool { return f.server.ActiveConnections() == 0 })
		})
	}
}

func TestSlowHandshakeWriterHasDeadline(t *testing.T) {
	f := setup(t, nil)
	f.stop() // Reuse configuration on a directly owned pipe, with no listener.
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
	frame, err := protocol.EncodeHello(protocol.Hello{Version: 1, Capabilities: []protocol.Capability{}, MaxPayloadSize: protocol.MaxPayloadSize})
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	// Never read HELLO_ACK, so the pipe's write cannot complete.
	var timeout net.Error
	if err := await(t, done); !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("blocked handshake write lacked timeout: %v", err)
	}
	if f.server.ActiveConnections() != 0 {
		t.Fatal("timed-out writer retained connection")
	}
}

func TestPeerDisappearsDuringAuthentication(t *testing.T) {
	f := setup(t, nil)
	conn := rawTLS(t, f)
	hello(t, conn)
	transport.Close(conn)
	eventually(t, func() bool { return f.server.ActiveConnections() == 0 })
	session, err := f.client.Connect(context.Background(), f.address, f.token)
	if err != nil {
		t.Fatal("disappeared peer retained a connection slot")
	}
	session.Close()
}

type rejectingVerifier struct{}

func (rejectingVerifier) Verify(_ context.Context, token string) (auth.Identity, error) {
	return auth.Identity{}, errors.New("backend failure for credential " + token)
}

func TestVerifierErrorsCannotDiscloseCredentials(t *testing.T) {
	f := setup(t, func(server *relay.Server, _ []auth.Record) { server.Authenticator = rejectingVerifier{} })
	_, err := f.client.Connect(context.Background(), f.address, f.token)
	var rejected *agent.AuthenticationError
	if !errors.As(err, &rejected) || rejected.Code != protocol.AuthInvalid || strings.Contains(err.Error(), f.token) {
		t.Fatal("verifier error was not redacted")
	}
}

func TestRelayEnforcesExpiryWithoutAgentWait(t *testing.T) {
	f := setup(t, func(_ *relay.Server, records []auth.Record) { records[0].ExpiresAt = time.Now().Add(time.Second) })
	conn := rawTLS(t, f)
	hello(t, conn)
	frame, err := protocol.EncodeAuth(protocol.Auth{Token: f.token})
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.DecodeAuthOK(response); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, conn)
}
