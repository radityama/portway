package protocol

import (
	"encoding/binary"
	"errors"
)

const (
	InitialStreamWindow     uint32 = 64 * 1024
	InitialConnectionWindow uint32 = 1024 * 1024
	WindowUpdateSize               = 4
)

var ErrInvalidWindow = errors.New("invalid flow-control window update")

func windowLimit(id uint64) uint32 {
	if id == 0 {
		return InitialConnectionWindow
	}
	return InitialStreamWindow
}

func EncodeWindowUpdate(id uint64, increment uint32) (Frame, error) {
	if increment == 0 || increment > windowLimit(id) {
		return Frame{}, ErrInvalidWindow
	}
	payload := make([]byte, WindowUpdateSize)
	binary.BigEndian.PutUint32(payload, increment)
	return Frame{Version: Version, Type: TypeWindowUpdate, StreamID: id, Payload: payload}, nil
}

func DecodeWindowUpdate(frame Frame) (uint32, error) {
	if err := frame.Validate(); err != nil {
		return 0, err
	}
	if frame.Type != TypeWindowUpdate {
		return 0, ErrUnexpectedType
	}
	increment := binary.BigEndian.Uint32(frame.Payload)
	if increment == 0 || increment > windowLimit(frame.StreamID) {
		return 0, ErrInvalidWindow
	}
	return increment, nil
}
