package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
)

func TestWindowUpdateSharedFixtures(t *testing.T) {
	fixtures := loadFixtures(t)
	flow := fixtures.FlowControl
	if flow.InitialStreamWindow != InitialStreamWindow || flow.InitialConnectionWindow != InitialConnectionWindow || flow.WindowUpdateSize != WindowUpdateSize {
		t.Fatal("flow-control limits differ from shared fixtures")
	}
	updates := []wireFixture{flow.ConnectionUpdate}
	for _, fixture := range fixtures.Frames {
		if fixture.Type == uint8(TypeWindowUpdate) {
			updates = append(updates, fixture)
		}
	}
	for _, fixture := range updates {
		id, _ := strconv.ParseUint(fixture.StreamID, 10, 64)
		wire, _ := hex.DecodeString(fixture.WireHex)
		frame, err := Decode(bytes.NewReader(wire))
		if err != nil {
			t.Fatal(err)
		}
		delta, err := DecodeWindowUpdate(frame)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := EncodeWindowUpdate(id, delta)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := encoded.Encode(&got); err != nil || !bytes.Equal(got.Bytes(), wire) {
			t.Fatal("binary credit fixture changed")
		}
	}
}

func TestWindowUpdateBoundsAndHeaderValidation(t *testing.T) {
	for _, id := range []uint64{0, 1, ^uint64(0)} {
		for _, delta := range []uint32{1, windowLimit(id)} {
			frame, err := EncodeWindowUpdate(id, delta)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeWindowUpdate(frame)
			if err != nil || got != delta {
				t.Fatal("valid credit boundary changed")
			}
		}
		for _, delta := range []uint32{0, windowLimit(id) + 1, ^uint32(0)} {
			if _, err := EncodeWindowUpdate(id, delta); !errors.Is(err, ErrInvalidWindow) {
				t.Fatal("invalid outbound credit accepted")
			}
			payload := make([]byte, 4)
			binary.BigEndian.PutUint32(payload, delta)
			if _, err := DecodeWindowUpdate(Frame{Version: 1, Type: TypeWindowUpdate, StreamID: id, Payload: payload}); !errors.Is(err, ErrInvalidWindow) {
				t.Fatal("invalid peer credit accepted")
			}
		}
	}
	for _, length := range []uint32{0, 3, 5, MaxPayloadSize} {
		header := validDataHeader(length)
		header[1] = byte(TypeWindowUpdate)
		reader := &headerReader{header: header}
		if _, err := Decode(reader); !errors.Is(err, ErrInvalidWindow) || reader.bodyReads != 0 {
			t.Fatal("invalid credit length allocated or read a payload")
		}
	}
}
