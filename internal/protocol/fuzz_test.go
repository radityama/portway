package protocol

import (
	"bytes"
	"encoding/hex"
	"slices"
	"testing"
)

func FuzzDecode(f *testing.F) {
	for _, fixture := range loadFixtures(f).Frames {
		wire, err := hex.DecodeString(fixture.WireHex)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(wire)
		f.Add(wire[:HeaderSize-1])
	}
	f.Add([]byte{})
	f.Add(validDataHeader(^uint32(0)))
	f.Fuzz(func(t *testing.T, wire []byte) {
		// Exercise the configured limit while keeping every fuzz allocation small.
		reader := bytes.NewReader(wire)
		frame, err := DecodeWithLimit(reader, 64*1024)
		if err != nil {
			return
		}
		if err := frame.Validate(); err != nil {
			t.Fatalf("decoder returned an invalid frame: %v", err)
		}
		var encoded bytes.Buffer
		if err := frame.Encode(&encoded); err != nil {
			t.Fatal(err)
		}
		consumed := len(wire) - reader.Len()
		if !bytes.Equal(encoded.Bytes(), wire[:consumed]) {
			t.Fatal("decoder changed wire bytes or consumed the next frame")
		}
	})
}

func FuzzFrameRoundTrip(f *testing.F) {
	f.Add(uint8(1), uint8(0x13), uint8(0), uint64(1), []byte("hello"))
	f.Add(uint8(0), uint8(0x09), uint8(0), uint64(0), []byte("{}"))
	f.Add(uint8(1), uint8(0x13), uint8(0), ^uint64(0), []byte{})
	f.Fuzz(func(t *testing.T, version, frameType, flags uint8, streamID uint64, payload []byte) {
		if len(payload) > 64*1024 {
			return
		}
		frame := Frame{Version: version, Type: Type(frameType), Flags: flags, StreamID: streamID, Payload: payload}
		var wire bytes.Buffer
		if err := frame.Encode(&wire); err != nil {
			if wire.Len() != 0 {
				t.Fatal("invalid frame wrote bytes")
			}
			return
		}
		if frame.Version == 0 {
			frame.Version = Version
		}
		decoded, err := Decode(&wire)
		if err != nil {
			t.Fatal(err)
		}
		assertFrameEqual(t, decoded, frame)
	})
}

func FuzzHandshake(f *testing.F) {
	f.Add([]byte(`{"version":1,"capabilities":[],"max_payload_size":4194304}`))
	f.Add([]byte(`{"version":1,"capabilities":["heartbeat"],"required_capabilities":["heartbeat"],"max_payload_size":1024}`))
	f.Add([]byte(`{"version":1,"capabilities":["future_feature"],"max_payload_size":1}`))
	f.Add([]byte(`{"version":1,"version":2,"capabilities":null,"max_payload_size":0}`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, payload []byte) {
		frame := Frame{Version: Version, Type: TypeHello, Payload: payload}
		if hello, err := DecodeHello(frame); err == nil {
			encoded, err := EncodeHello(hello)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeHello(encoded)
			if err != nil || decoded.Version != hello.Version || decoded.MaxPayloadSize != hello.MaxPayloadSize || !slices.Equal(decoded.Capabilities, hello.Capabilities) || !slices.Equal(decoded.RequiredCapabilities, hello.RequiredCapabilities) {
				t.Fatalf("HELLO canonical round trip failed: %v", err)
			}
		}
		frame.Type = TypeHelloAck
		if ack, err := DecodeHelloAck(frame); err == nil {
			encoded, err := EncodeHelloAck(ack)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeHelloAck(encoded)
			if err != nil || decoded.Version != ack.Version || decoded.MaxPayloadSize != ack.MaxPayloadSize || !slices.Equal(decoded.Capabilities, ack.Capabilities) {
				t.Fatalf("HELLO_ACK canonical round trip failed: %v", err)
			}
		}
	})
}
