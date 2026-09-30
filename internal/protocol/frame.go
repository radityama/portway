package protocol

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	Version        uint8  = 1
	HeaderSize            = 16
	MaxPayloadSize uint32 = 4 * 1024 * 1024
)

type Type uint8

const (
	TypeHello Type = 0x01 + iota
	TypeHelloAck
	TypeAuth
	TypeAuthOK
	TypeAuthError
	TypeRegister
	TypeRegisterOK
	TypeRegisterError
	TypePing
	TypePong
	TypeOpenStream
	TypeOpenStreamOK
	TypeOpenStreamError
	TypeData
	TypeWindowUpdate
	TypeCloseStream
	TypeResetStream
	TypeGoAway
)

type Frame struct {
	Version  uint8
	Type     Type
	Flags    uint8
	StreamID uint64
	Payload  []byte
}

func (f Frame) Encode(w io.Writer) error {
	if f.Version == 0 {
		f.Version = Version
	}
	if err := f.Validate(); err != nil {
		return err
	}
	header := make([]byte, HeaderSize)
	header[0] = f.Version
	header[1] = byte(f.Type)
	header[2] = f.Flags
	binary.BigEndian.PutUint64(header[4:12], f.StreamID)
	binary.BigEndian.PutUint32(header[12:16], uint32(len(f.Payload)))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if len(f.Payload) > 0 {
		if _, err := w.Write(f.Payload); err != nil {
			return fmt.Errorf("write frame payload: %w", err)
		}
	}
	return nil
}

func Decode(r *bufio.Reader) (Frame, error) {
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return Frame{}, fmt.Errorf("read frame header: %w", err)
	}
	if header[0] != Version {
		return Frame{}, fmt.Errorf("unsupported protocol version %d", header[0])
	}
	if !Type(header[1]).Valid() {
		return Frame{}, fmt.Errorf("unknown frame type 0x%02x", header[1])
	}
	if header[3] != 0 {
		return Frame{}, fmt.Errorf("reserved header byte must be zero")
	}
	length := binary.BigEndian.Uint32(header[12:16])
	if length > MaxPayloadSize {
		return Frame{}, fmt.Errorf("payload too large: %d", length)
	}
	payload := make([]byte, int(length))
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, fmt.Errorf("read frame payload: %w", err)
		}
	}
	return Frame{
		Version:  header[0],
		Type:     Type(header[1]),
		Flags:    header[2],
		StreamID: binary.BigEndian.Uint64(header[4:12]),
		Payload:  payload,
	}, nil
}
