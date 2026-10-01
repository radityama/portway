package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func websocketOpen() OpenStream {
	return OpenStream{Method: "GET", Target: "/ws", Host: "p-abc.portway.localhost", ContentLength: 0, Upgrade: "websocket", Headers: [][]string{{"Sec-Websocket-Key", "dGhlIHNhbXBsZSBub25jZQ=="}, {"Sec-Websocket-Version", "13"}, {"Sec-Websocket-Protocol", "chat, other"}}}
}

func TestWebSocketMetadataRoundTripStrictFieldsAndLegacyWire(t *testing.T) {
	good := websocketOpen()
	frame, err := EncodeOpenStream(1, good)
	if err != nil {
		t.Fatal(err)
	}
	fixture := loadFixtures(t).WebSocket.Open
	wire, _ := hex.DecodeString(fixture.WireHex)
	var encoded bytes.Buffer
	if err := frame.Encode(&encoded); err != nil || !bytes.Equal(encoded.Bytes(), wire) {
		t.Fatal("shared upgrade fixture differs")
	}
	value, err := DecodeOpenStream(frame)
	if err != nil || value.Upgrade != "websocket" {
		t.Fatal("upgrade roundtrip failed")
	}
	for name, mutate := range map[string]func(*OpenStream){
		"upgrade":   func(o *OpenStream) { o.Upgrade = "h2c" },
		"method":    func(o *OpenStream) { o.Method = "POST" },
		"body":      func(o *OpenStream) { o.ContentLength = -1 },
		"key":       func(o *OpenStream) { o.Headers[0][1] = strings.Repeat("x", 24) },
		"version":   func(o *OpenStream) { o.Headers[1][1] = "12" },
		"duplicate": func(o *OpenStream) { o.Headers = append(o.Headers, o.Headers[0]) },
		"extensions": func(o *OpenStream) {
			o.Headers = append(o.Headers, []string{"Sec-Websocket-Extensions", "permessage-deflate"})
		},
		"protocol": func(o *OpenStream) { o.Headers[2][1] = "chat, chat" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := websocketOpen()
			mutate(&bad)
			if _, err := EncodeOpenStream(1, bad); err == nil {
				t.Fatal("invalid upgrade encoded")
			}
			data, _ := json.Marshal(bad)
			frame.Payload = data
			if _, err := DecodeOpenStream(frame); err == nil {
				t.Fatal("invalid upgrade decoded")
			}
		})
	}
	ordinary := OpenStream{Method: "GET", Target: "/", Host: "a.example", Headers: [][]string{}, ContentLength: 0}
	frame, err = EncodeOpenStream(1, ordinary)
	if err != nil || bytes.Contains(frame.Payload, []byte("upgrade")) {
		t.Fatal("legacy HTTP wire changed")
	}
	base := strings.TrimSuffix(string(frame.Payload), "}")
	for _, extra := range []string{`,"upgrade":null}`, `,"upgrade":""}`, `,"upgrade":"h2c"}`, `,"upgrade":"websocket","upgrade":"websocket"}`} {
		frame.Payload = []byte(base + extra)
		if _, err := DecodeOpenStream(frame); err == nil {
			t.Fatal("malformed optional field accepted")
		}
	}
}

func TestStreamingCapabilitiesNegotiateWithoutBreakingOldPeers(t *testing.T) {
	newPeer := helloOffer(CapabilityMultiplexing, CapabilityFlowControl, CapabilityStreaming, CapabilityWebSocket)
	oldPeer := helloOffer(CapabilityMultiplexing, CapabilityFlowControl)
	ack, err := Negotiate(newPeer, oldPeer)
	if err != nil || len(ack.Capabilities) != 2 || ack.ValidateFor(newPeer) != nil || ack.ValidateFor(oldPeer) != nil {
		t.Fatal("old peer compatibility failed")
	}
	newPeer.RequiredCapabilities = []Capability{CapabilityWebSocket}
	if _, err := Negotiate(newPeer, oldPeer); err == nil {
		t.Fatal("missing required websocket accepted")
	}
}
