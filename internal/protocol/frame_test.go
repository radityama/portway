package protocol

import (
	"bufio"
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	original := Frame{Version: Version, Type: TypeData, Flags: 1, StreamID: 42, Payload: []byte("hello")}
	var buf bytes.Buffer
	if err := original.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.StreamID != original.StreamID || decoded.Type != original.Type || string(decoded.Payload) != string(original.Payload) {
		t.Fatalf("decoded frame mismatch: %#v", decoded)
	}
}
