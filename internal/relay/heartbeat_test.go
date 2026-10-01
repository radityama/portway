package relay_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/radityama/portway/internal/protocol"
)

func TestNegotiatedHeartbeatOverTLSAndDiagnosticCleanup(t *testing.T) {
	f := setup(t, nil)
	conn := rawTLS(t, f)
	hello, _ := protocol.EncodeHello(protocol.Hello{Version: protocol.Version, Capabilities: []protocol.Capability{protocol.CapabilityHeartbeat}, MaxPayloadSize: protocol.MaxPayloadSize})
	if err := hello.Encode(conn); err != nil {
		t.Fatal(err)
	}
	frame, err := protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := protocol.DecodeHelloAck(frame)
	if err != nil || !slices.Contains(ack.Capabilities, protocol.CapabilityHeartbeat) {
		t.Fatal("relay did not negotiate heartbeat")
	}
	auth, _ := protocol.EncodeAuth(protocol.Auth{Token: f.token})
	if err := auth.Encode(conn); err != nil {
		t.Fatal(err)
	}
	frame, err = protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.DecodeAuthOK(frame); err != nil {
		t.Fatal(err)
	}
	register, _ := protocol.EncodeRegister(protocol.Register{TunnelID: "tnl_local_dev", Generation: 1})
	if err := register.Encode(conn); err != nil {
		t.Fatal(err)
	}
	frame, err = protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := protocol.DecodeRegisterOK(frame)
	if err != nil || registered.PublicURL != "" {
		t.Fatal("diagnostic heartbeat enabled a public data-plane route")
	}
	ping, _ := protocol.EncodeHeartbeat(protocol.TypePing, protocol.Heartbeat{Nonce: "0123456789abcdef", Timestamp: "2026-10-01T00:00:00Z"})
	if err := ping.Encode(conn); err != nil {
		t.Fatal(err)
	}
	pong, err := protocol.DecodeTypes(conn, protocol.MaxHandshakePayloadSize, protocol.TypePong)
	if err != nil || !bytes.Equal(ping.Payload, pong.Payload) {
		t.Fatal("relay did not echo validated PING over TLS")
	}
	if _, exists := f.server.Lookup(registered.PublicHostname); !exists {
		t.Fatal("valid heartbeat lost its owner")
	}
	// Relay has no outstanding probe yet. An unsolicited PONG is terminal.
	ping.Type = protocol.TypePong
	_ = ping.Encode(conn)
	if _, err := protocol.Decode(conn); err == nil {
		t.Fatal("relay accepted unsolicited PONG")
	}
	eventually(t, func() bool {
		_, exists := f.server.Lookup(registered.PublicHostname)
		return !exists && f.server.ActiveConnections() == 0
	})
}
