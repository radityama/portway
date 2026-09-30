package protocol

import (
	"errors"
	"fmt"
)

var (
	ErrUnsupportedVersion = errors.New("unsupported protocol version")
	ErrUnknownType        = errors.New("unknown frame type")
	ErrUnexpectedType     = errors.New("unexpected frame type for connection state")
	ErrInvalidFlags       = errors.New("v1 frame flags must be zero")
	ErrReservedByte       = errors.New("reserved header byte must be zero")
	ErrInvalidStreamID    = errors.New("invalid stream ID for frame type")
	ErrPayloadTooLarge    = errors.New("frame payload exceeds limit")
	ErrInvalidLimit       = errors.New("payload limit must be between 1 and 4194304")
)

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
	return f.validateHeader(uint64(len(f.Payload)), MaxPayloadSize)
}

// IsStream reports whether the type addresses a logical stream, not a connection.
func (t Type) IsStream() bool {
	return t >= TypeOpenStream && t <= TypeResetStream
}

func validateLimit(limit uint32) error {
	if limit == 0 || limit > MaxPayloadSize {
		return ErrInvalidLimit
	}
	return nil
}

func (f Frame) validateHeader(length uint64, limit uint32) error {
	if err := validateLimit(limit); err != nil {
		return err
	}
	if f.Version != Version {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, f.Version)
	}
	if !f.Type.Valid() {
		return fmt.Errorf("%w: 0x%02x", ErrUnknownType, uint8(f.Type))
	}
	if f.Flags != 0 {
		return ErrInvalidFlags
	}
	if f.Type.IsStream() != (f.StreamID != 0) {
		return ErrInvalidStreamID
	}
	if length > uint64(limit) {
		return fmt.Errorf("%w: %d > %d", ErrPayloadTooLarge, length, limit)
	}
	if f.Type >= TypeHello && f.Type <= TypeRegisterError && length > MaxHandshakePayloadSize {
		return ErrPayloadTooLarge
	}
	return nil
}
