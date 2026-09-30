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
	Frames                  []wireFixture     `json:"frames"`
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
