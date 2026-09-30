package protocol

import "fmt"

func (t Type) Valid() bool {
	switch t {
	case TypeHello, TypeHelloAck,
		TypeAuth, TypeAuthOK, TypeAuthError,
		TypeRegister, TypeRegisterOK, TypeRegisterError,
		TypePing, TypePong,
		TypeOpenStream, TypeOpenStreamOK, TypeOpenStreamError,
		TypeData, TypeWindowUpdate, TypeCloseStream, TypeResetStream,
		TypeGoAway:
		return true
	default:
		return false
	}
}

func (f Frame) Validate() error {
	if f.Version != Version {
		return fmt.Errorf("unsupported protocol version %d", f.Version)
	}
	if !f.Type.Valid() {
		return fmt.Errorf("unknown frame type 0x%02x", uint8(f.Type))
	}
	if uint64(len(f.Payload)) > uint64(MaxPayloadSize) {
		return fmt.Errorf("payload too large: %d", len(f.Payload))
	}
	return nil
}
