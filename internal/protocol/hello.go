package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
)

const (
	MaxHandshakePayloadSize = 4096
	MaxCapabilities         = 32
	MaxCapabilityNameSize   = 64
)

type Capability string

const (
	CapabilityMultiplexing     Capability = "multiplexing"
	CapabilityFlowControl      Capability = "flow_control"
	CapabilityHeartbeat        Capability = "heartbeat"
	CapabilityGracefulShutdown Capability = "graceful_shutdown"
)

var (
	ErrInvalidHandshake      = errors.New("invalid handshake payload")
	ErrUnsupportedCapability = errors.New("required capability is unavailable")
)

// Hello describes what a peer supports and what it requires from the other peer.
// Advertise a capability only when its behavior is actually implemented.
type Hello struct {
	Version              uint8        `json:"version"`
	Capabilities         []Capability `json:"capabilities"`
	RequiredCapabilities []Capability `json:"required_capabilities,omitempty"`
	MaxPayloadSize       uint32       `json:"max_payload_size"`
}

type HelloAck struct {
	Version        uint8        `json:"version"`
	Capabilities   []Capability `json:"capabilities"`
	MaxPayloadSize uint32       `json:"max_payload_size"`
}

func (h Hello) Validate() error {
	if err := validateOffer(h.Version, h.Capabilities, h.MaxPayloadSize); err != nil {
		return err
	}
	if err := validateCapabilities(h.RequiredCapabilities); err != nil {
		return err
	}
	for _, required := range h.RequiredCapabilities {
		if !slices.Contains(h.Capabilities, required) {
			return ErrInvalidHandshake
		}
	}
	return nil
}

func (h HelloAck) Validate() error {
	if err := validateOffer(h.Version, h.Capabilities, h.MaxPayloadSize); err != nil {
		return err
	}
	if !slices.IsSorted(h.Capabilities) {
		return ErrInvalidHandshake
	}
	return nil
}

// ValidateFor prevents a peer from selecting capabilities or limits not offered.
func (h HelloAck) ValidateFor(offer Hello) error {
	if err := offer.Validate(); err != nil {
		return err
	}
	if err := h.Validate(); err != nil {
		return err
	}
	if h.MaxPayloadSize > offer.MaxPayloadSize {
		return ErrInvalidHandshake
	}
	for _, selected := range h.Capabilities {
		if !slices.Contains(offer.Capabilities, selected) {
			return ErrInvalidHandshake
		}
	}
	for _, required := range offer.RequiredCapabilities {
		if !slices.Contains(h.Capabilities, required) {
			return ErrUnsupportedCapability
		}
	}
	return nil
}

// Negotiate returns a deterministic intersection without modifying either offer.
func Negotiate(remote, local Hello) (HelloAck, error) {
	if err := remote.Validate(); err != nil {
		return HelloAck{}, err
	}
	if err := local.Validate(); err != nil {
		return HelloAck{}, err
	}
	ack := HelloAck{
		Version:        Version,
		Capabilities:   make([]Capability, 0),
		MaxPayloadSize: min(remote.MaxPayloadSize, local.MaxPayloadSize),
	}
	for _, capability := range remote.Capabilities {
		if slices.Contains(local.Capabilities, capability) {
			ack.Capabilities = append(ack.Capabilities, capability)
		}
	}
	slices.Sort(ack.Capabilities)
	if err := ack.ValidateFor(remote); err != nil {
		return HelloAck{}, err
	}
	if err := ack.ValidateFor(local); err != nil {
		return HelloAck{}, err
	}
	return ack, nil
}

func EncodeHello(h Hello) (Frame, error) {
	if err := h.Validate(); err != nil {
		return Frame{}, err
	}
	if h.Capabilities == nil {
		h.Capabilities = []Capability{}
	}
	return encodeHandshake(TypeHello, h)
}

func DecodeHello(f Frame) (Hello, error) {
	var hello Hello
	if err := validateHandshakeFrame(f, TypeHello); err != nil {
		return Hello{}, err
	}
	if err := decodeHandshake(f.Payload, &hello); err != nil {
		return Hello{}, err
	}
	if err := hello.Validate(); err != nil {
		return Hello{}, err
	}
	return hello, nil
}

func EncodeHelloAck(h HelloAck) (Frame, error) {
	if err := h.Validate(); err != nil {
		return Frame{}, err
	}
	if h.Capabilities == nil {
		h.Capabilities = []Capability{}
	}
	return encodeHandshake(TypeHelloAck, h)
}

func DecodeHelloAck(f Frame) (HelloAck, error) {
	var ack HelloAck
	if err := validateHandshakeFrame(f, TypeHelloAck); err != nil {
		return HelloAck{}, err
	}
	if err := decodeHandshake(f.Payload, &ack); err != nil {
		return HelloAck{}, err
	}
	if err := ack.Validate(); err != nil {
		return HelloAck{}, err
	}
	return ack, nil
}

func validateOffer(version uint8, capabilities []Capability, limit uint32) error {
	if version != Version {
		return ErrUnsupportedVersion
	}
	if err := validateLimit(limit); err != nil {
		return err
	}
	return validateCapabilities(capabilities)
}

func validateCapabilities(capabilities []Capability) error {
	if len(capabilities) > MaxCapabilities {
		return ErrInvalidHandshake
	}
	seen := make(map[Capability]bool, len(capabilities))
	for _, capability := range capabilities {
		if len(capability) == 0 || len(capability) > MaxCapabilityNameSize || seen[capability] {
			return ErrInvalidHandshake
		}
		for i := range len(capability) {
			c := capability[i]
			if (c < 'a' || c > 'z') && (i == 0 || ((c < '0' || c > '9') && c != '_')) {
				return ErrInvalidHandshake
			}
		}
		seen[capability] = true
	}
	return nil
}

func encodeHandshake(frameType Type, value any) (Frame, error) {
	payload, err := json.Marshal(value)
	if err != nil || len(payload) > MaxHandshakePayloadSize {
		return Frame{}, ErrInvalidHandshake
	}
	return Frame{Version: Version, Type: frameType, Payload: payload}, nil
}

func validateHandshakeFrame(f Frame, expected Type) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if f.Type != expected || len(f.Payload) > MaxHandshakePayloadSize {
		return ErrInvalidHandshake
	}
	return nil
}

// Check duplicate and missing keys before typed decoding. The standard JSON
// decoder otherwise accepts duplicate keys and maps null arrays to nil slices.
// Never return decoder errors that could include peer-supplied payload contents.
func decodeHandshake(payload []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalidHandshake
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return ErrInvalidHandshake
		}
		key, ok := token.(string)
		if !ok {
			return ErrInvalidHandshake
		}
		switch key {
		case "version", "capabilities", "required_capabilities", "max_payload_size":
		default:
			return ErrInvalidHandshake
		}
		if _, exists := fields[key]; exists {
			return ErrInvalidHandshake
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return ErrInvalidHandshake
		}
		fields[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return ErrInvalidHandshake
	}
	for _, required := range []string{"version", "capabilities", "max_payload_size"} {
		if _, exists := fields[required]; !exists {
			return ErrInvalidHandshake
		}
	}
	for _, key := range []string{"capabilities", "required_capabilities"} {
		if raw, exists := fields[key]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrInvalidHandshake
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrInvalidHandshake
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ErrInvalidHandshake
	}
	return nil
}
