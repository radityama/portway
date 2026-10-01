package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
)

func TestGoAwayContract(t *testing.T) {
	for _, code := range []string{GoAwayShutdown, GoAwayDrained} {
		frame, err := EncodeGoAway(GoAway{Code: code})
		if err != nil {
			t.Fatal(err)
		}
		value, err := DecodeGoAway(frame)
		if err != nil || value.Code != code {
			t.Fatal("GOAWAY round trip changed")
		}
	}
	for _, f := range loadFixtures(t).Frames {
		if f.Name != "GOAWAY" {
			continue
		}
		payload, _ := hex.DecodeString(f.PayloadHex)
		frame, _ := EncodeGoAway(GoAway{Code: GoAwayShutdown})
		if !bytes.Equal(payload, frame.Payload) {
			t.Fatal("GOAWAY fixture differs")
		}
	}
	for _, payload := range []string{`{}`, `null`, `[]`, `{"code":null}`, `{"code":3}`, `{"code":"secret"}`, `{"code":"SHUTDOWN","extra":1}`, `{"code":"SHUTDOWN","code":"DRAINED"}`, `{"code":"SHUTDOWN"}{}`} {
		_, err := DecodeGoAway(Frame{Version: Version, Type: TypeGoAway, Payload: []byte(payload)})
		if !errors.Is(err, ErrInvalidGoAway) {
			t.Fatal("malformed GOAWAY accepted")
		}
	}
	if _, err := EncodeGoAway(GoAway{Code: "unknown"}); err == nil {
		t.Fatal("unknown code encoded")
	}
	wire := make([]byte, HeaderSize)
	wire[0], wire[1] = Version, byte(TypeGoAway)
	binary.BigEndian.PutUint32(wire[12:], MaxHandshakePayloadSize+1)
	if _, err := Decode(bytes.NewReader(wire)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatal("oversized GOAWAY allocated payload")
	}
	frame, _ := EncodeGoAway(GoAway{Code: GoAwayShutdown})
	frame.StreamID = 1
	if _, err := DecodeGoAway(frame); err == nil {
		t.Fatal("stream-level GOAWAY accepted")
	}
}
