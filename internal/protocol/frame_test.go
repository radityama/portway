package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, []byte("hello"), {0, 255, 128}} {
		original := Frame{Version: Version, Type: TypeData, StreamID: 42, Payload: payload}
		var buf bytes.Buffer
		if err := original.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(&buf)
		if err != nil {
			t.Fatal(err)
		}
		assertFrameEqual(t, decoded, original)
	}
}

func TestUnsetEncodeVersionUsesV1(t *testing.T) {
	var buf bytes.Buffer
	if err := (Frame{Type: TypePing}).Encode(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.Bytes()[0] != 1 {
		t.Fatal("encoder must write wire version 1")
	}
	if err := (Frame{Type: TypePing}).Validate(); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Validate must reject an unset wire version: %v", err)
	}
}

func TestFragmentedAndConsecutiveFrames(t *testing.T) {
	frames := []Frame{
		{Version: Version, Type: TypePing, Payload: []byte(`{}`)},
		{Version: Version, Type: TypeData, StreamID: 1, Payload: []byte("fragmented")},
		{Version: Version, Type: TypeGoAway},
	}
	var buf bytes.Buffer
	for _, frame := range frames {
		if err := frame.Encode(&buf); err != nil {
			t.Fatal(err)
		}
	}
	reader := fragmentReader{Reader: &buf, size: 1}
	for _, expected := range frames {
		decoded, err := Decode(reader)
		if err != nil {
			t.Fatal(err)
		}
		assertFrameEqual(t, decoded, expected)
	}
	if _, err := Decode(reader); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after last frame: %v", err)
	}
}

func TestEveryTruncation(t *testing.T) {
	wire := []byte{1, 0x13, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 3, 1, 2, 3}
	for cut := range len(wire) {
		_, err := Decode(bytes.NewReader(wire[:cut]))
		expected := io.ErrUnexpectedEOF
		if cut == 0 || cut == HeaderSize {
			expected = io.EOF
		}
		if !errors.Is(err, expected) {
			t.Fatalf("cut %d: expected %v, got %v", cut, expected, err)
		}
	}
}

func TestPayloadLimitBoundaries(t *testing.T) {
	for _, limit := range []uint32{3, MaxDataSize} {
		frame := Frame{Version: Version, Type: TypeData, StreamID: 1, Payload: make([]byte, limit)}
		var buf bytes.Buffer
		if err := frame.EncodeWithLimit(&buf, limit); err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeWithLimit(&buf, limit)
		if err != nil {
			t.Fatal(err)
		}
		assertFrameEqual(t, decoded, frame)
		frame.Payload = append(frame.Payload, 0)
		if err := frame.EncodeWithLimit(io.Discard, limit); !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("limit %d: expected oversized rejection, got %v", limit, err)
		}
	}
}

func TestInvalidLimitsDoNotPerformIO(t *testing.T) {
	for _, limit := range []uint32{0, MaxPayloadSize + 1, ^uint32(0)} {
		reader := &headerReader{header: validDataHeader(0)}
		if _, err := DecodeWithLimit(reader, limit); !errors.Is(err, ErrInvalidLimit) || reader.reads != 0 {
			t.Fatalf("invalid limit must reject before reading: %v, %d reads", err, reader.reads)
		}
		var buf bytes.Buffer
		if err := (Frame{Type: TypePing}).EncodeWithLimit(&buf, limit); !errors.Is(err, ErrInvalidLimit) || buf.Len() != 0 {
			t.Fatalf("invalid limit must reject before writing: %v", err)
		}
	}
}

func TestInvalidHeadersNeverReadPayload(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]byte)
		want   error
	}{
		{"zero version", func(h []byte) { h[0] = 0 }, ErrUnsupportedVersion},
		{"future version", func(h []byte) { h[0] = 2 }, ErrUnsupportedVersion},
		{"unknown type", func(h []byte) { h[1] = 0xff }, ErrUnknownType},
		{"unassigned type", func(h []byte) { h[1] = 0x0b }, ErrUnknownType},
		{"flags", func(h []byte) { h[2] = 1 }, ErrInvalidFlags},
		{"reserved", func(h []byte) { h[3] = 1 }, ErrReservedByte},
		{"zero stream ID", func(h []byte) { clear(h[4:12]) }, ErrInvalidStreamID},
		{"connection stream ID", func(h []byte) { h[1] = 0x09 }, ErrInvalidStreamID},
		{"global limit", func(h []byte) { binary.BigEndian.PutUint32(h[12:], MaxPayloadSize+1) }, ErrPayloadTooLarge},
		{"length overflow", func(h []byte) { binary.BigEndian.PutUint32(h[12:], ^uint32(0)) }, ErrPayloadTooLarge},
		{"handshake limit", func(h []byte) {
			h[1] = 0x01
			clear(h[4:12])
			binary.BigEndian.PutUint32(h[12:], MaxHandshakePayloadSize+1)
		}, ErrPayloadTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := validDataHeader(1)
			tc.mutate(header)
			reader := &headerReader{header: header}
			if _, err := Decode(reader); !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
			if reader.bodyReads != 0 {
				t.Fatal("invalid header caused a payload read")
			}
		})
	}
	reader := &headerReader{header: validDataHeader(4)}
	if _, err := DecodeWithLimit(reader, 3); !errors.Is(err, ErrPayloadTooLarge) || reader.bodyReads != 0 {
		t.Fatalf("configured limit must reject before reading body: %v", err)
	}
}

func TestEncoderCompletesPartialWrites(t *testing.T) {
	frame := Frame{Type: TypeData, StreamID: 7, Payload: []byte("payload")}
	var expected bytes.Buffer
	if err := frame.Encode(&expected); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	writer := writerFunc(func(p []byte) (int, error) { return got.Write(p[:min(3, len(p))]) })
	if err := frame.Encode(writer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), expected.Bytes()) {
		t.Fatal("partial writes lost or duplicated bytes")
	}
}

func TestEncoderRejectsBrokenWriters(t *testing.T) {
	for _, n := range []int{-1, 0, HeaderSize + 1} {
		writer := writerFunc(func([]byte) (int, error) { return n, nil })
		if err := (Frame{Type: TypePing}).Encode(writer); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("writer returned %d: expected short-write error, got %v", n, err)
		}
	}
}

func TestIOErrorsArePreservedAndNotRetried(t *testing.T) {
	for _, failCall := range []int{1, 2} {
		calls := 0
		writer := writerFunc(func(p []byte) (int, error) {
			calls++
			if calls == failCall {
				return 1, io.ErrClosedPipe
			}
			return len(p), nil
		})
		if err := (Frame{Type: TypeData, StreamID: 1, Payload: []byte("ab")}).Encode(writer); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("write error not preserved: %v", err)
		}
		if calls != failCall {
			t.Fatal("encoder retried after a write error")
		}
	}
	reader := readerFunc(func([]byte) (int, error) { return 0, io.ErrClosedPipe })
	if _, err := Decode(reader); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("read error not preserved: %v", err)
	}
}

func assertFrameEqual(t *testing.T, got, want Frame) {
	t.Helper()
	if got.Version != want.Version || got.Type != want.Type || got.Flags != want.Flags || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("frame mismatch: got %#v, want %#v", got, want)
	}
}

func validDataHeader(length uint32) []byte {
	header := []byte{1, 0x13, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[12:], length)
	return header
}

type fragmentReader struct {
	io.Reader
	size int
}

func (r fragmentReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:min(r.size, len(p))]) }

type headerReader struct {
	header    []byte
	reads     int
	bodyReads int
}

func (r *headerReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.header) == 0 {
		r.bodyReads++
		return 0, errors.New("unexpected payload read")
	}
	n := copy(p, r.header)
	r.header = r.header[n:]
	return n, nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
