package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/radityama/portway/internal/devsetup"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

func TestRegisteredAgentWaitHandlesHeartbeatOverTLS(t *testing.T) {
	dir := t.TempDir()
	if err := devsetup.Ensure(dir, false); err != nil {
		t.Fatal(err)
	}
	serverConfig, err := transport.ServerConfig(filepath.Join(dir, "relay-cert.pem"), filepath.Join(dir, "relay-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	clientConfig, err := transport.ClientConfig(filepath.Join(dir, "ca.pem"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- func() error {
			conn := tls.Server(a, serverConfig)
			defer a.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			if err := conn.HandshakeContext(ctx); err != nil {
				return err
			}
			frame, err := protocol.Decode(conn)
			if err != nil {
				return err
			}
			hello, err := protocol.DecodeHello(frame)
			if err != nil {
				return err
			}
			ack, err := protocol.Negotiate(hello, protocol.Hello{Version: protocol.Version, Capabilities: []protocol.Capability{protocol.CapabilityHeartbeat}, MaxPayloadSize: protocol.MaxPayloadSize})
			if err != nil {
				return err
			}
			frame, _ = protocol.EncodeHelloAck(ack)
			if err := frame.Encode(conn); err != nil {
				return err
			}
			if _, err := protocol.DecodeTypes(conn, protocol.MaxHandshakePayloadSize, protocol.TypeAuth); err != nil {
				return err
			}
			id := "con_0123456789abcdef0123456789abcdef"
			frame, _ = protocol.EncodeAuthOK(protocol.AuthOK{ConnectionID: id, ExpiresAt: time.Now().Add(time.Minute)})
			if err := frame.Encode(conn); err != nil {
				return err
			}
			frame, err = protocol.DecodeTypes(conn, protocol.MaxHandshakePayloadSize, protocol.TypeRegister)
			if err != nil {
				return err
			}
			request, err := protocol.DecodeRegister(frame)
			if err != nil {
				return err
			}
			frame, _ = protocol.EncodeRegisterOK(protocol.RegisterOK{TunnelID: request.TunnelID, Generation: request.Generation, ConnectionID: id, PublicHostname: "p-abc.portway.localhost"})
			if err := frame.Encode(conn); err != nil {
				return err
			}
			ping, _ := protocol.EncodeHeartbeat(protocol.TypePing, protocol.Heartbeat{Nonce: "0123456789abcdef", Timestamp: "2026-10-01T00:00:00Z"})
			if err := ping.Encode(conn); err != nil {
				return err
			}
			pong, err := protocol.DecodeTypes(conn, protocol.MaxHandshakePayloadSize, protocol.TypePong)
			if err != nil {
				return err
			}
			if !bytes.Equal(ping.Payload, pong.Payload) {
				return protocol.ErrInvalidHeartbeat
			}
			ping.Type = protocol.TypePong
			if err := ping.Encode(conn); err != nil {
				return err
			}
			return nil
		}()
	}()
	conn := tls.Client(b, clientConfig)
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	client := NewClient(&plainTransport{conn: conn})
	session, err := client.Connect(ctx, "unused", "fixture_credential_only_for_tests")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Register(ctx, protocol.Register{TunnelID: "tnl_fixture", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	if err := session.Wait(); !errors.Is(err, protocol.ErrInvalidHeartbeat) {
		t.Fatalf("agent heartbeat state accepted unsolicited PONG: %v", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("TLS heartbeat peer retained")
	}
}
