package protocol

import "testing"

func TestInvalidTypeRejected(t *testing.T) {
	frame := Frame{Version: Version, Type: Type(0xff)}
	if err := frame.Validate(); err == nil {
		t.Fatal("expected invalid type to be rejected")
	}
}
