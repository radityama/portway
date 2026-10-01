package protocol

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestStreamControlGoldenFixtures(t *testing.T) {
	for _, fixture := range loadFixtures(t).Frames {
		payload, _ := hex.DecodeString(fixture.PayloadHex)
		f := Frame{Version: 1, Type: Type(fixture.Type), StreamID: 1, Payload: payload}
		var encoded Frame
		var err error
		switch f.Type {
		case TypeOpenStream:
			request, decodeErr := DecodeOpenStream(f)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			encoded, err = EncodeOpenStream(1, request)
		case TypeOpenStreamOK, TypeOpenStreamError, TypeCloseStream, TypeResetStream:
			code, decodeErr := DecodeStreamControl(f)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			encoded, err = EncodeStreamControl(f.Type, 1, code)
		default:
			continue
		}
		if err != nil || string(encoded.Payload) != string(payload) {
			t.Fatal("noncanonical stream fixture")
		}
	}
}
func TestStrictStreamMetadataAndCaps(t *testing.T) {
	good := OpenStream{Method: "POST", Target: "/echo?x=a%2Fb", Host: "p-abc.portway.localhost", Headers: [][]string{{"Content-Type", "application/json"}}, ContentLength: -1}
	for _, mutate := range []func(*OpenStream){func(o *OpenStream) { o.Method = "CONNECT" }, func(o *OpenStream) { o.Target = "http://victim.example/" }, func(o *OpenStream) { o.Target = "/bad\r\nHost:x" }, func(o *OpenStream) { o.Host = "attacker@host" }, func(o *OpenStream) { o.Headers = [][]string{{"Connection", "upgrade"}} }, func(o *OpenStream) { o.Headers = [][]string{{"X-Header", "injected\r\n"}} }, func(o *OpenStream) { o.Headers = [][]string{{"X-Header", "a", "extra"}} }, func(o *OpenStream) { o.ContentLength = MaxRequestBodySize + 1 }, func(o *OpenStream) { o.Headers = nil }} {
		request := good
		mutate(&request)
		if _, err := EncodeOpenStream(1, request); err == nil {
			t.Fatal("invalid stream metadata accepted")
		}
	}
	for _, payload := range []string{`{}`, `{"method":"GET","method":"POST","target":"/","host":"a.example","headers":[],"content_length":0}`, `{"method":"GET","target":"/","host":"a.example","headers":null,"content_length":0}`, `{"method":"GET","target":"/","host":"a.example","headers":[],"content_length":0,"upstream":"victim"}`} {
		if _, err := DecodeOpenStream(Frame{Version: 1, Type: TypeOpenStream, StreamID: 1, Payload: []byte(payload)}); err == nil {
			t.Fatal("malformed OPEN accepted")
		}
	}
	good.Headers = [][]string{{"X-Header", strings.Repeat("x", MaxHTTPHeaderSize)}}
	if _, err := EncodeOpenStream(1, good); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatal("header size not bounded")
	}
	for _, typ := range []Type{TypeOpenStream, TypeData, TypeCloseStream, TypeResetStream} {
		limit := uint32(MaxHandshakePayloadSize)
		if typ == TypeData {
			limit = MaxDataSize
		}
		if typ == TypeOpenStream {
			limit = MaxOpenPayloadSize
		}
		header := validDataHeader(limit + 1)
		header[1] = byte(typ)
		reader := &headerReader{header: header}
		if _, err := Decode(reader); !errors.Is(err, ErrPayloadTooLarge) || reader.bodyReads != 0 {
			t.Fatal("stream payload bound allocated a body")
		}
	}
}
func FuzzStreamPayloads(f *testing.F) {
	for _, fixture := range loadFixtures(f).Frames {
		if fixture.Type >= 0x10 && fixture.Type <= 0x16 {
			payload, _ := hex.DecodeString(fixture.PayloadHex)
			f.Add(payload)
		}
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		frame := Frame{Version: 1, Type: TypeOpenStream, StreamID: 1, Payload: payload}
		if value, err := DecodeOpenStream(frame); err == nil {
			encoded, err := EncodeOpenStream(1, value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeOpenStream(encoded); err != nil {
				t.Fatal(err)
			}
		}
		for _, typ := range []Type{TypeOpenStreamOK, TypeOpenStreamError, TypeCloseStream, TypeResetStream} {
			frame.Type = typ
			if code, err := DecodeStreamControl(frame); err == nil {
				encoded, err := EncodeStreamControl(typ, 1, code)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := DecodeStreamControl(encoded)
				if err != nil || decoded != code {
					t.Fatal("stream control round trip")
				}
			}
		}
	})
}
