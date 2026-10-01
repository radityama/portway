package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"testing"
)

type wireFixture struct {
	Name       string `json:"name"`
	Type       uint8  `json:"type"`
	StreamID   string `json:"stream_id"`
	PayloadHex string `json:"payload_hex"`
	WireHex    string `json:"wire_hex"`
}

type protocolFixtures struct {
	Version                 uint8             `json:"version"`
	HeaderSize              int               `json:"header_size"`
	MaxPayloadSize          uint32            `json:"max_payload_size"`
	MaxHandshakePayloadSize int               `json:"max_handshake_payload_size"`
	MaxCapabilities         int               `json:"max_capabilities"`
	MaxCapabilityNameSize   int               `json:"max_capability_name_size"`
	Capabilities            map[string]string `json:"capabilities"`
	FlowControl             struct {
		InitialStreamWindow     uint32      `json:"initial_stream_window"`
		InitialConnectionWindow uint32      `json:"initial_connection_window"`
		WindowUpdateSize        int         `json:"window_update_size"`
		ConnectionUpdate        wireFixture `json:"connection_update"`
	} `json:"flow_control"`
	CustomDomains struct {
		Open wireFixture `json:"open"`
	} `json:"custom_domains"`
	Frames    []wireFixture `json:"frames"`
	WebSocket struct {
		Open wireFixture `json:"open"`
	} `json:"websocket"`
}

func loadFixtures(t testing.TB) protocolFixtures {
	t.Helper()
	data, err := os.ReadFile("../../tests/fixtures/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures protocolFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func wireTypesByValue() map[Type]string {
	return map[Type]string{
		TypeHello: "HELLO", TypeHelloAck: "HELLO_ACK", TypeAuth: "AUTH",
		TypeAuthOK: "AUTH_OK", TypeAuthError: "AUTH_ERROR", TypeRegister: "REGISTER",
		TypeRegisterOK: "REGISTER_OK", TypeRegisterError: "REGISTER_ERROR",
		TypePing: "PING", TypePong: "PONG", TypeOpenStream: "OPEN_STREAM",
		TypeOpenStreamOK: "OPEN_STREAM_OK", TypeOpenStreamError: "OPEN_STREAM_ERROR",
		TypeData: "DATA", TypeWindowUpdate: "WINDOW_UPDATE", TypeCloseStream: "CLOSE_STREAM",
		TypeResetStream: "RESET_STREAM", TypeGoAway: "GOAWAY",
	}
}

func TestGoldenWireFixtures(t *testing.T) {
	fixtures := loadFixtures(t)
	if fixtures.Version != Version || fixtures.HeaderSize != HeaderSize || fixtures.MaxPayloadSize != MaxPayloadSize || fixtures.MaxHandshakePayloadSize != MaxHandshakePayloadSize || fixtures.MaxCapabilities != MaxCapabilities || fixtures.MaxCapabilityNameSize != MaxCapabilityNameSize {
		t.Fatal("Go protocol limits differ from shared fixtures")
	}
	capabilities := map[string]Capability{
		"MULTIPLEXING": CapabilityMultiplexing, "FLOW_CONTROL": CapabilityFlowControl,
		"HEARTBEAT": CapabilityHeartbeat, "GRACEFUL_SHUTDOWN": CapabilityGracefulShutdown,
		"STREAMING": CapabilityStreaming, "WEBSOCKET": CapabilityWebSocket, "CUSTOM_DOMAINS": CapabilityCustomDomains,
	}
	if len(capabilities) != len(fixtures.Capabilities) {
		t.Fatal("capability fixture mismatch")
	}
	for name, capability := range capabilities {
		if fixtures.Capabilities[name] != string(capability) {
			t.Fatalf("capability mismatch: %s", name)
		}
	}
	types := wireTypesByValue()
	seen := map[Type]bool{}
	for _, fixture := range fixtures.Frames {
		t.Run(fixture.Name, func(t *testing.T) {
			frameType := Type(fixture.Type)
			if types[frameType] != fixture.Name || seen[frameType] {
				t.Fatal("Go type differs from fixture or fixture is duplicated")
			}
			seen[frameType] = true
			id, err := strconv.ParseUint(fixture.StreamID, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := hex.DecodeString(fixture.PayloadHex)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := hex.DecodeString(fixture.WireHex)
			if err != nil {
				t.Fatal(err)
			}
			frame := Frame{Version: Version, Type: frameType, StreamID: id, Payload: payload}
			var encoded bytes.Buffer
			if err := frame.Encode(&encoded); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded.Bytes(), wire) {
				t.Fatalf("wire differs: got %x, want %x", encoded.Bytes(), wire)
			}
			decoded, err := Decode(bytes.NewReader(wire))
			if err != nil {
				t.Fatal(err)
			}
			assertFrameEqual(t, decoded, frame)
		})
	}
	if len(seen) != len(types) {
		t.Fatal("fixtures must cover every message type")
	}
}

func TestDocumentedWireTypes(t *testing.T) {
	doc, err := os.ReadFile("../../docs/PROTOCOL.md")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`(?m)^0x([0-9A-F]{2}) ([A-Z_]+)$`).FindAllSubmatch(doc, -1)
	types := wireTypesByValue()
	if len(matches) != len(types) {
		t.Fatal("protocol documentation must list every message type")
	}
	for _, match := range matches {
		value, err := strconv.ParseUint(string(match[1]), 16, 8)
		if err != nil || types[Type(value)] != string(match[2]) {
			t.Fatalf("documented type differs from Go: %s", match[0])
		}
	}
}

func TestAliasGoldenWireFixture(t *testing.T) {
	fixture := loadFixtures(t).CustomDomains.Open
	wire, err := hex.DecodeString(fixture.WireHex)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := Decode(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	open, err := DecodeOpenStream(frame)
	if err != nil || open.PublicHost != "app.example.test" || open.Host != "p-bound.portway.localhost" {
		t.Fatal("alias fixture mismatch", err)
	}
	encoded, err := EncodeOpenStream(1, open)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := encoded.Encode(&output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, output.Bytes()) {
		t.Fatal("alias wire mismatch")
	}
	offer := Hello{Version: Version, Capabilities: []Capability{CapabilityMultiplexing, CapabilityCustomDomains}, MaxPayloadSize: MaxPayloadSize}
	legacy := Hello{Version: Version, Capabilities: []Capability{CapabilityMultiplexing}, MaxPayloadSize: MaxPayloadSize}
	ack, err := Negotiate(offer, legacy)
	if err != nil || len(ack.Capabilities) != 1 || ack.Capabilities[0] != CapabilityMultiplexing {
		t.Fatal("legacy negotiation changed", err)
	}
}
