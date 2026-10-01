package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatSharedFixtures(t *testing.T) {
	data, err := os.ReadFile("../../tests/fixtures/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Heartbeat struct {
			IntervalMS int64 `json:"interval_ms"`
			TimeoutMS  int64 `json:"timeout_ms"`
		} `json:"heartbeat"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if HeartbeatInterval/time.Millisecond != time.Duration(fixture.Heartbeat.IntervalMS) || HeartbeatTimeout/time.Millisecond != time.Duration(fixture.Heartbeat.TimeoutMS) {
		t.Fatal("Go heartbeat timing differs from shared fixtures")
	}
	for _, fixture := range loadFixtures(t).Frames {
		if fixture.Type != uint8(TypePing) && fixture.Type != uint8(TypePong) {
			continue
		}
		payload, _ := hex.DecodeString(fixture.PayloadHex)
		f := Frame{Version: Version, Type: Type(fixture.Type), Payload: payload}
		h, err := DecodeHeartbeat(f)
		if err != nil {
			t.Fatal(err)
		}
		if h.Nonce != "0123456789abcdef" || h.Timestamp != "2026-10-01T00:00:00Z" {
			t.Fatal("heartbeat fixture contract differs")
		}
		encoded, err := EncodeHeartbeat(f.Type, h)
		if err != nil || !bytes.Equal(encoded.Payload, payload) {
			t.Fatal("heartbeat canonical fixture differs")
		}
	}
}

func TestHeartbeatRejectsMalformedInput(t *testing.T) {
	valid := `{"nonce":"0123456789abcdef","timestamp":"2026-10-01T00:00:00Z"}`
	for _, payload := range []string{
		`{}`, `null`, valid + `{}`, `{"nonce":null,"timestamp":"2026-10-01T00:00:00Z"}`,
		strings.Replace(valid, `"nonce":`, `"nonce":"0123456789abcdef","nonce":`, 1),
		strings.Replace(valid, `abcdef`, `ABCDEF`, 1),
		strings.Replace(valid, `0123456789abcdef`, `123`, 1),
		strings.Replace(valid, `2026-10-01T00:00:00Z`, `2026-10-01T00:00:00+00:00`, 1),
		strings.Replace(valid, `2026-10-01T00:00:00Z`, `0001-01-01T00:00:00Z`, 1),
		strings.Replace(valid, `"timestamp":`, `"extra":1,"timestamp":`, 1),
		strings.Replace(valid, `"2026-10-01T00:00:00Z"`, `42`, 1),
	} {
		for _, typ := range []Type{TypePing, TypePong} {
			_, err := DecodeHeartbeat(Frame{Version: Version, Type: typ, Payload: []byte(payload)})
			if err == nil || strings.Contains(err.Error(), payload) {
				t.Fatal("invalid/sensitive heartbeat input accepted or echoed")
			}
		}
	}
	if _, err := EncodeHeartbeat(TypeData, Heartbeat{}); err == nil {
		t.Fatal("non-heartbeat type accepted")
	}
	for _, typ := range []Type{TypePing, TypePong} {
		wire := make([]byte, HeaderSize)
		wire[0], wire[1] = Version, byte(typ)
		binary.BigEndian.PutUint32(wire[12:], MaxHandshakePayloadSize+1)
		_, err := Decode(bytes.NewReader(wire))
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatal("oversized heartbeat tried to read its payload")
		}
	}
}
