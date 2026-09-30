package protocol

import (
	"encoding/json"
	"strconv"
	"strings"
)

const MaxTunnelIDSize = 128

// Generation uses decimal strings on the wire to preserve uint64 in JavaScript.
type Generation uint64

func ParseGeneration(value string) (Generation, error) {
	if len(value) == 0 || len(value) > 20 || value[0] < '1' || value[0] > '9' {
		return 0, ErrInvalidHandshake
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return 0, ErrInvalidHandshake
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, ErrInvalidHandshake
	}
	return Generation(n), nil
}

func (g Generation) String() string               { return strconv.FormatUint(uint64(g), 10) }
func (g Generation) MarshalJSON() ([]byte, error) { return json.Marshal(g.String()) }
func (g *Generation) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return ErrInvalidHandshake
	}
	n, err := ParseGeneration(value)
	if err != nil {
		return err
	}
	*g = n
	return nil
}

func ValidTunnelID(id string) bool {
	if len(id) == 0 || len(id) > MaxTunnelIDSize {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ValidHostname accepts canonical DNS names, excluding numeric/IP hosts.
func ValidHostname(host string) bool {
	if len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	letter := false
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			c := label[i]
			if c >= 'a' && c <= 'z' {
				letter = true
			} else if (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return letter
}

type Register struct {
	TunnelID   string     `json:"tunnel_id"`
	Generation Generation `json:"generation"`
}

type RegisterOK struct {
	TunnelID       string     `json:"tunnel_id"`
	ConnectionID   string     `json:"connection_id"`
	Generation     Generation `json:"generation"`
	PublicHostname string     `json:"public_hostname"`
}

type RegisterError struct {
	Code string `json:"code"`
}

const (
	RegisterInvalid   = "REGISTER_INVALID"
	RegisterForbidden = "REGISTER_FORBIDDEN"
	RegisterStale     = "REGISTER_STALE"
	RegisterCapacity  = "REGISTER_CAPACITY"
	RegisterConflict  = "REGISTER_CONFLICT"
)

func (value Register) Validate() error {
	if !ValidTunnelID(value.TunnelID) || value.Generation == 0 {
		return ErrInvalidHandshake
	}
	return nil
}

func (value RegisterOK) Validate() error {
	if err := (Register{TunnelID: value.TunnelID, Generation: value.Generation}).Validate(); err != nil {
		return err
	}
	if !validConnectionID(value.ConnectionID) || !ValidHostname(value.PublicHostname) {
		return ErrInvalidHandshake
	}
	return nil
}

func (value RegisterOK) ValidateFor(request Register, connectionID string) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.TunnelID != request.TunnelID || value.Generation != request.Generation || value.ConnectionID != connectionID {
		return ErrInvalidHandshake
	}
	return nil
}

func EncodeRegister(value Register) (Frame, error) {
	if err := value.Validate(); err != nil {
		return Frame{}, err
	}
	return encodeHandshake(TypeRegister, value)
}
func DecodeRegister(frame Frame) (Register, error) {
	var value Register
	if err := validateHandshakeFrame(frame, TypeRegister); err != nil {
		return value, err
	}
	keys := []string{"tunnel_id", "generation"}
	if err := decodeObject(frame.Payload, &value, keys, keys, keys); err != nil {
		return Register{}, err
	}
	if err := value.Validate(); err != nil {
		return Register{}, err
	}
	return value, nil
}
func EncodeRegisterOK(value RegisterOK) (Frame, error) {
	if err := value.Validate(); err != nil {
		return Frame{}, err
	}
	return encodeHandshake(TypeRegisterOK, value)
}
func DecodeRegisterOK(frame Frame) (RegisterOK, error) {
	var value RegisterOK
	if err := validateHandshakeFrame(frame, TypeRegisterOK); err != nil {
		return value, err
	}
	keys := []string{"tunnel_id", "connection_id", "generation", "public_hostname"}
	if err := decodeObject(frame.Payload, &value, keys, keys, keys); err != nil {
		return RegisterOK{}, err
	}
	if err := value.Validate(); err != nil {
		return RegisterOK{}, err
	}
	return value, nil
}
func validRegisterCode(code string) bool {
	return code == RegisterInvalid || code == RegisterForbidden || code == RegisterStale || code == RegisterCapacity || code == RegisterConflict
}
func EncodeRegisterError(value RegisterError) (Frame, error) {
	if !validRegisterCode(value.Code) {
		return Frame{}, ErrInvalidHandshake
	}
	return encodeHandshake(TypeRegisterError, value)
}
func DecodeRegisterError(frame Frame) (RegisterError, error) {
	var value RegisterError
	if err := validateHandshakeFrame(frame, TypeRegisterError); err != nil {
		return value, err
	}
	keys := []string{"code"}
	if err := decodeObject(frame.Payload, &value, keys, keys, keys); err != nil {
		return RegisterError{}, err
	}
	if !validRegisterCode(value.Code) {
		return RegisterError{}, ErrInvalidHandshake
	}
	return value, nil
}
