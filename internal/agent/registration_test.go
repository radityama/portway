package agent

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/radityama/portway/internal/devsetup"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

func TestHTTPRegistrationRequiresBothStreamCapabilities(t *testing.T) {
	for _, session := range []*Session{{multiplexing: true}, {flowControl: true}, {}} {
		_, err := session.Register(context.Background(), protocol.Register{TunnelID: "tnl_fixture", Generation: 1, Protocol: "http"})
		if !errors.Is(err, protocol.ErrInvalidHandshake) || session.state.Load() != 0 {
			t.Fatal("HTTP registration proceeded without negotiated stream credits")
		}
	}
}

func TestAgentRejectsHostileRegistrationResponses(t *testing.T) {
	for _, kind := range []string{"wrong tunnel", "wrong connection", "wrong generation", "unsafe hostname", "unexpected header", "truncated", "disappeared", "timeout", "cancel"} {
		t.Run(kind, func(t *testing.T) {
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
			serverRaw, clientRaw := net.Pipe()
			defer serverRaw.Close()
			defer clientRaw.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				conn := tls.Server(serverRaw, serverConfig)
				defer serverRaw.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				if err := conn.HandshakeContext(ctx); err != nil {
					done <- err
					return
				}
				frame, err := protocol.Decode(conn)
				if err != nil {
					done <- err
					return
				}
				hello, err := protocol.DecodeHello(frame)
				if err != nil {
					done <- err
					return
				}
				ack, _ := protocol.EncodeHelloAck(protocol.HelloAck{Version: 1, Capabilities: []protocol.Capability{}, MaxPayloadSize: hello.MaxPayloadSize})
				if err := ack.Encode(conn); err != nil {
					done <- err
					return
				}
				if _, err := protocol.Decode(conn); err != nil {
					done <- err
					return
				}
				id := "con_0123456789abcdef0123456789abcdef"
				ack, _ = protocol.EncodeAuthOK(protocol.AuthOK{ConnectionID: id, ExpiresAt: time.Now().Add(time.Hour)})
				if err := ack.Encode(conn); err != nil {
					done <- err
					return
				}
				frame, err = protocol.Decode(conn)
				if err != nil {
					done <- err
					return
				}
				request, err := protocol.DecodeRegister(frame)
				if err != nil {
					done <- err
					return
				}
				response := protocol.RegisterOK{TunnelID: request.TunnelID, Generation: request.Generation, ConnectionID: id, PublicHostname: "p-abc.portway.localhost"}
				switch kind {
				case "wrong tunnel":
					response.TunnelID = "tnl_other"
				case "wrong connection":
					response.ConnectionID = "con_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				case "wrong generation":
					response.Generation++
				case "unsafe hostname":
					ack = protocol.Frame{Type: protocol.TypeRegisterOK, Payload: []byte(`{"tunnel_id":"tnl_fixture","generation":"1","connection_id":"con_0123456789abcdef0123456789abcdef","public_hostname":"credential@evil.example"}`)}
				case "unexpected header", "truncated":
					header := make([]byte, 16)
					header[0] = 1
					header[1] = byte(protocol.TypeAuthOK)
					if kind == "truncated" {
						header[1] = byte(protocol.TypeRegisterOK)
					}
					binary.BigEndian.PutUint32(header[12:], 100)
					_, err = conn.Write(header)
					done <- err
					return
				case "disappeared":
					done <- nil
					return
				case "cancel":
					cancel()
					done <- nil
					return
				case "timeout":
					_, err = conn.Read(make([]byte, 1))
					done <- nil
					return
				}
				if kind != "unsafe hostname" {
					ack, err = protocol.EncodeRegisterOK(response)
					if err != nil {
						done <- err
						return
					}
				}
				done <- ack.Encode(conn)
			}()
			clientConn := tls.Client(clientRaw, clientConfig)
			if err := clientConn.HandshakeContext(ctx); err != nil {
				t.Fatal(err)
			}
			client := NewClient(&plainTransport{conn: clientConn})
			client.RegistrationTimeout = 100 * time.Millisecond
			session, err := client.Connect(ctx, "unused", "fixture_credential_only_for_tests")
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, err := session.Register(ctx, protocol.Register{TunnelID: "tnl_fixture", Generation: 1}); err == nil {
				t.Fatal("hostile response accepted")
			}
			if _, err := session.Register(ctx, protocol.Register{TunnelID: "tnl_fixture", Generation: 2}); err != ErrSessionInUse {
				t.Fatal("failed session allowed registration retry")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("fake peer did not exit")
			}
		})
	}
}
