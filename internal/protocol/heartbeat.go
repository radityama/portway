package protocol

import (
	"errors"
	"time"
)

const HeartbeatInterval = 15 * time.Second
const HeartbeatTimeout = 45 * time.Second

var ErrInvalidHeartbeat = errors.New("invalid heartbeat payload")

type Heartbeat struct {
	Nonce     string `json:"nonce"`
	Timestamp string `json:"timestamp"`
}

func (h Heartbeat) Validate() error {
	if len(h.Nonce) != 16 || len(h.Timestamp) > 30 {
		return ErrInvalidHeartbeat
	}
	for _, c := range h.Nonce {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ErrInvalidHeartbeat
		}
	}
	t, err := time.Parse(time.RFC3339Nano, h.Timestamp)
	if err != nil || t.IsZero() || t.UTC().Format(time.RFC3339Nano) != h.Timestamp {
		return ErrInvalidHeartbeat
	}
	return nil
}

func EncodeHeartbeat(typ Type, h Heartbeat) (Frame, error) {
	if typ != TypePing && typ != TypePong {
		return Frame{}, ErrInvalidHeartbeat
	}
	if err := h.Validate(); err != nil {
		return Frame{}, err
	}
	return encodeHandshake(typ, h)
}

func DecodeHeartbeat(f Frame) (Heartbeat, error) {
	if f.Type != TypePing && f.Type != TypePong {
		return Heartbeat{}, ErrInvalidHeartbeat
	}
	if err := validateHandshakeFrame(f, f.Type); err != nil {
		return Heartbeat{}, err
	}
	var h Heartbeat
	keys := []string{"nonce", "timestamp"}
	if err := decodeObject(f.Payload, &h, keys, keys, keys); err != nil {
		return Heartbeat{}, ErrInvalidHeartbeat
	}
	if err := h.Validate(); err != nil {
		return Heartbeat{}, err
	}
	return h, nil
}
