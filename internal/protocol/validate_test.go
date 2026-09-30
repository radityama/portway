package protocol

import (
	"bytes"
	"errors"
	"testing"
)

func TestEveryTypeAndStreamID(t *testing.T) {
	for value := range 256 {
		frameType := Type(value)
		_, valid := wireTypesByValue()[frameType]
		if frameType.Valid() != valid {
			t.Fatalf("incorrect validity for type 0x%02x", value)
		}
		for _, id := range []uint64{0, 1, ^uint64(0)} {
			frame := Frame{Version: Version, Type: frameType, StreamID: id}
			err := frame.Validate()
			if !valid {
				if !errors.Is(err, ErrUnknownType) {
					t.Fatalf("type 0x%02x: %v", value, err)
				}
				continue
			}
			streamType := value >= 0x10 && value <= 0x16
			if streamType == (id != 0) {
				if err != nil {
					t.Fatalf("valid type/ID rejected: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidStreamID) {
				t.Fatalf("invalid type/ID accepted: %v", err)
			}
		}
	}
}

func TestInvalidFramesDoNotWrite(t *testing.T) {
	cases := []Frame{
		{Version: 2, Type: TypePing},
		{Version: Version, Type: 0xff},
		{Version: Version, Type: TypePing, Flags: 1},
		{Version: Version, Type: TypePing, StreamID: 1},
		{Version: Version, Type: TypeData},
		{Version: Version, Type: TypeData, StreamID: 1, Payload: make([]byte, MaxPayloadSize+1)},
		{Version: Version, Type: TypeHello, Payload: make([]byte, MaxHandshakePayloadSize+1)},
	}
	for _, frame := range cases {
		var buf bytes.Buffer
		if err := frame.Encode(&buf); err == nil || buf.Len() != 0 {
			t.Fatalf("invalid frame wrote bytes or succeeded: %v", err)
		}
	}
}
