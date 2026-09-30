package protocol

import (
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
	TypeHello           Type = 0x01
	TypeHelloAck        Type = 0x02
	TypeAuth            Type = 0x03
	TypeAuthOK          Type = 0x04
	TypeAuthError       Type = 0x05
	TypeRegister        Type = 0x06
	TypeRegisterOK      Type = 0x07
	TypeRegisterError   Type = 0x08
	TypePing            Type = 0x09
	TypePong            Type = 0x0A
	TypeOpenStream      Type = 0x10
	TypeOpenStreamOK    Type = 0x11
	TypeOpenStreamError Type = 0x12
	TypeData            Type = 0x13
	TypeWindowUpdate    Type = 0x14
	TypeCloseStream     Type = 0x15
	TypeResetStream     Type = 0x16
	TypeGoAway          Type = 0x17
)

type Frame struct {
	Version  uint8
	Type     Type
	Flags    uint8
	StreamID uint64
	Payload  []byte
}

func (f Frame) Encode(w io.Writer) error {
	return f.EncodeWithLimit(w, MaxPayloadSize)
}

// EncodeWithLimit writes one complete frame. An error may leave a partial frame
// on the wire; the caller must close the connection rather than retry the frame.
func (f Frame) EncodeWithLimit(w io.Writer, maxPayloadSize uint32) error {
	if f.Version == 0 {
		f.Version = Version
	}
	if err := f.validateHeader(uint64(len(f.Payload)), maxPayloadSize); err != nil {
		return err
	}
	var header [HeaderSize]byte
	header[0] = f.Version
	header[1] = byte(f.Type)
	header[2] = f.Flags
	binary.BigEndian.PutUint64(header[4:12], f.StreamID)
	binary.BigEndian.PutUint32(header[12:16], uint32(len(f.Payload)))
	if err := writeAll(w, header[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if err := writeAll(w, f.Payload); err != nil {
		return fmt.Errorf("write frame payload: %w", err)
	}
	return nil
}

func Decode(r io.Reader) (Frame, error) {
	return DecodeWithLimit(r, MaxPayloadSize)
}

// DecodeWithLimit validates the complete header before allocating the payload.
// Callers own cancellation and deadlines on the underlying reader.
func DecodeWithLimit(r io.Reader, maxPayloadSize uint32) (Frame, error) {
	if err := validateLimit(maxPayloadSize); err != nil {
		return Frame{}, err
	}
	var header [HeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, fmt.Errorf("read frame header: %w", err)
	}
	if header[3] != 0 {
		return Frame{}, ErrReservedByte
	}
	length := binary.BigEndian.Uint32(header[12:16])
	f := Frame{
		Version:  header[0],
		Type:     Type(header[1]),
		Flags:    header[2],
		StreamID: binary.BigEndian.Uint64(header[4:12]),
	}
	if err := f.validateHeader(uint64(length), maxPayloadSize); err != nil {
		return Frame{}, err
	}
	if length > 0 {
		f.Payload = make([]byte, int(length))
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return Frame{}, fmt.Errorf("read frame payload: %w", err)
		}
	}
	return f, nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
