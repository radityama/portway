package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func helloOffer(capabilities ...Capability) Hello {
	return Hello{Version: Version, Capabilities: capabilities, MaxPayloadSize: MaxPayloadSize}
}

func TestHelloCodecsAndFixtures(t *testing.T) {
	for _, fixture := range loadFixtures(t).Frames {
		if fixture.Name != "HELLO" && fixture.Name != "HELLO_ACK" {
			continue
		}
		wire, err := hex.DecodeString(fixture.WireHex)
		if err != nil {
			t.Fatal(err)
		}
		frame, err := Decode(bytes.NewReader(wire))
		if err != nil {
			t.Fatal(err)
		}
		var encoded Frame
		if fixture.Name == "HELLO" {
			value, decodeErr := DecodeHello(frame)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			encoded, err = EncodeHello(value)
		} else {
			value, decodeErr := DecodeHelloAck(frame)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			encoded, err = EncodeHelloAck(value)
		}
		if err != nil {
			t.Fatal(err)
		}
		assertFrameEqual(t, encoded, frame)
	}
	// Local nil slices are encoded as empty arrays, never null.
	frame, err := EncodeHello(helloOffer())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(frame.Payload), "null") {
		t.Fatal("encoder produced null capabilities")
	}
	hello := helloOffer(CapabilityHeartbeat, CapabilityMultiplexing)
	hello.RequiredCapabilities = []Capability{CapabilityMultiplexing}
	frame, err = EncodeHello(hello)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeHello(frame)
	if err != nil || !reflect.DeepEqual(decoded, hello) {
		t.Fatalf("HELLO round trip failed: %v", err)
	}
}

func TestNegotiateCapabilitiesAndLimits(t *testing.T) {
	remote := helloOffer(CapabilityMultiplexing, "remote_extension", CapabilityFlowControl)
	remote.RequiredCapabilities = []Capability{CapabilityMultiplexing}
	local := helloOffer(CapabilityFlowControl, CapabilityHeartbeat, CapabilityMultiplexing)
	local.RequiredCapabilities = []Capability{CapabilityFlowControl}
	local.MaxPayloadSize = 65536
	remoteBefore := slices.Clone(remote.Capabilities)
	localBefore := slices.Clone(local.Capabilities)
	ack, err := Negotiate(remote, local)
	if err != nil {
		t.Fatal(err)
	}
	want := HelloAck{Version: Version, Capabilities: []Capability{CapabilityFlowControl, CapabilityMultiplexing}, MaxPayloadSize: 65536}
	if !reflect.DeepEqual(ack, want) {
		t.Fatalf("negotiation mismatch: %#v", ack)
	}
	if !slices.Equal(remoteBefore, remote.Capabilities) || !slices.Equal(localBefore, local.Capabilities) {
		t.Fatal("negotiation modified an offer")
	}
	reversed, err := Negotiate(local, remote)
	if err != nil || !reflect.DeepEqual(ack, reversed) {
		t.Fatalf("negotiation must be symmetric: %v", err)
	}
	frame, err := EncodeHelloAck(ack)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeHelloAck(frame)
	if err != nil || decoded.ValidateFor(remote) != nil || decoded.ValidateFor(local) != nil {
		t.Fatalf("negotiated ack failed validation: %v", err)
	}
}

func TestUnknownAndEmptyCapabilities(t *testing.T) {
	for _, pair := range [][2]Hello{
		{helloOffer(), helloOffer()},
		{helloOffer("future_feature"), helloOffer(CapabilityHeartbeat)},
		{helloOffer("future_feature"), helloOffer("future_feature")},
	} {
		ack, err := Negotiate(pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if slices.Equal(pair[0].Capabilities, []Capability{"future_feature"}) && slices.Equal(pair[1].Capabilities, pair[0].Capabilities) {
			want = 1
		}
		if len(ack.Capabilities) != want {
			t.Fatalf("unexpected optional capability selection: %v", ack.Capabilities)
		}
	}
}

func TestRequiredCapabilitiesFromEitherPeer(t *testing.T) {
	requiring := helloOffer(CapabilityHeartbeat)
	requiring.RequiredCapabilities = []Capability{CapabilityHeartbeat}
	for _, pair := range [][2]Hello{{requiring, helloOffer()}, {helloOffer(), requiring}} {
		if _, err := Negotiate(pair[0], pair[1]); !errors.Is(err, ErrUnsupportedCapability) {
			t.Fatalf("missing requirement must fail: %v", err)
		}
	}
	invalid := helloOffer()
	invalid.RequiredCapabilities = []Capability{CapabilityHeartbeat}
	if _, err := Negotiate(invalid, requiring); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatalf("unadvertised requirement must fail: %v", err)
	}
}

func TestNegotiationRejectsInvalidOffers(t *testing.T) {
	cases := []Hello{
		{Version: 0, MaxPayloadSize: MaxPayloadSize},
		{Version: 2, MaxPayloadSize: MaxPayloadSize},
		{Version: Version, MaxPayloadSize: 0},
		{Version: Version, MaxPayloadSize: MaxPayloadSize + 1},
		helloOffer("heartbeat", "heartbeat"),
		helloOffer(""), helloOffer("Uppercase"), helloOffer("1bad"),
		helloOffer("bad-name"), helloOffer("bad name"), helloOffer("unicode_é"),
		helloOffer(Capability(strings.Repeat("a", MaxCapabilityNameSize+1))),
	}
	tooMany := helloOffer()
	for i := range MaxCapabilities + 1 {
		tooMany.Capabilities = append(tooMany.Capabilities, Capability("cap_"+string(rune('a'+i/26))+string(rune('a'+i%26))))
	}
	cases = append(cases, tooMany)
	for _, invalid := range cases {
		for _, pair := range [][2]Hello{{invalid, helloOffer()}, {helloOffer(), invalid}} {
			if _, err := Negotiate(pair[0], pair[1]); err == nil {
				t.Fatalf("invalid offer accepted: %#v", invalid)
			}
		}
		if _, err := EncodeHello(invalid); err == nil {
			t.Fatal("invalid offer encoded")
		}
	}
}

func TestCapabilityBoundariesAndHandshakeSize(t *testing.T) {
	hello := helloOffer()
	for i := range MaxCapabilities {
		hello.Capabilities = append(hello.Capabilities, Capability(strings.Repeat("a", MaxCapabilityNameSize-2)+string(rune('a'+i/26))+string(rune('a'+i%26))))
	}
	if _, err := EncodeHello(hello); err != nil {
		t.Fatalf("maximum capability count/name size rejected: %v", err)
	}
	// Individually valid lists must also fit the total payload bound.
	hello.RequiredCapabilities = slices.Clone(hello.Capabilities)
	if _, err := EncodeHello(hello); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatalf("oversized handshake encoded: %v", err)
	}
	hello.RequiredCapabilities = nil
	frame, err := EncodeHello(hello)
	if err != nil {
		t.Fatal(err)
	}
	frame.Payload = append(frame.Payload, bytes.Repeat([]byte(" "), MaxHandshakePayloadSize-len(frame.Payload))...)
	if _, err := DecodeHello(frame); err != nil {
		t.Fatalf("handshake at exact limit rejected: %v", err)
	}
	frame.Payload = append(frame.Payload, ' ')
	if _, err := DecodeHello(frame); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("handshake above limit accepted: %v", err)
	}
}

func TestMalformedHandshakePayloads(t *testing.T) {
	valid := `{"version":1,"capabilities":[],"max_payload_size":4194304}`
	cases := []string{
		``, `{`, `null`, `[]`, `{}`, valid + `{}`, valid + `true`,
		`{"version":1,"version":1,"capabilities":[],"max_payload_size":4194304}`,
		`{"version":1,"Version":1,"capabilities":[],"max_payload_size":4194304}`,
		`{"version":1,"capabilities":[],"max_payload_size":4194304,"secret_value":"do_not_log_me"}`,
		`{"version":1,"capabilities":null,"max_payload_size":4194304}`,
		`{"version":1,"capabilities":[],"required_capabilities":null,"max_payload_size":4194304}`,
		`{"version":1,"capabilities":[123],"max_payload_size":4194304}`,
		`{"version":"1","capabilities":[],"max_payload_size":4194304}`,
		`{"version":1.0,"capabilities":[],"max_payload_size":4194304}`,
		`{"version":1,"capabilities":[],"max_payload_size":-1}`,
		`{"version":1,"capabilities":[],"max_payload_size":4294967296}`,
		`{"version":1,"capabilities":[]}`,
		`{"version":1,"max_payload_size":4194304}`,
		`{"capabilities":[],"max_payload_size":4194304}`,
	}
	for _, payload := range cases {
		for _, frameType := range []Type{TypeHello, TypeHelloAck} {
			frame := Frame{Version: Version, Type: frameType, Payload: []byte(payload)}
			var err error
			if frameType == TypeHello {
				_, err = DecodeHello(frame)
			} else {
				_, err = DecodeHelloAck(frame)
			}
			if err == nil {
				t.Fatalf("malformed handshake accepted: %q", payload)
			}
			if strings.Contains(err.Error(), "do_not_log_me") || strings.Contains(err.Error(), "secret_value") {
				t.Fatal("handshake error disclosed payload contents")
			}
		}
	}
}

func TestHandshakeFrameTypeAndEnvelope(t *testing.T) {
	hello, err := EncodeHello(helloOffer())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeHelloAck(hello); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatalf("wrong frame type accepted: %v", err)
	}
	for _, mutate := range []func(*Frame){
		func(f *Frame) { f.Type = TypePing },
		func(f *Frame) { f.Version = 2 },
		func(f *Frame) { f.Flags = 1 },
		func(f *Frame) { f.StreamID = 1 },
	} {
		frame := hello
		mutate(&frame)
		if _, err := DecodeHello(frame); err == nil {
			t.Fatal("invalid handshake envelope accepted")
		}
	}
}

func TestAckMustMatchOffer(t *testing.T) {
	offer := helloOffer(CapabilityFlowControl, CapabilityHeartbeat)
	offer.MaxPayloadSize = 1024
	offer.RequiredCapabilities = []Capability{CapabilityHeartbeat}
	cases := []HelloAck{
		{Version: 2, MaxPayloadSize: 1024},
		{Version: Version, MaxPayloadSize: 2048},
		{Version: Version, MaxPayloadSize: 1024},
		{Version: Version, MaxPayloadSize: 1024, Capabilities: []Capability{CapabilityHeartbeat, CapabilityMultiplexing}},
		{Version: Version, MaxPayloadSize: 1024, Capabilities: []Capability{CapabilityHeartbeat, CapabilityFlowControl}},
		{Version: Version, MaxPayloadSize: 1024, Capabilities: []Capability{CapabilityHeartbeat, CapabilityHeartbeat}},
	}
	for _, ack := range cases {
		if err := ack.ValidateFor(offer); err == nil {
			t.Fatalf("invalid ack accepted: %#v", ack)
		}
		payload, err := json.Marshal(ack)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeHelloAck(Frame{Version: Version, Type: TypeHelloAck, Payload: payload})
		if err == nil && decoded.ValidateFor(offer) == nil {
			t.Fatal("invalid wire ack accepted")
		}
	}
	valid := HelloAck{Version: Version, MaxPayloadSize: 512, Capabilities: []Capability{CapabilityHeartbeat}}
	if err := valid.ValidateFor(offer); err != nil {
		t.Fatal(err)
	}
}
