package protocol

import "time"

const (
	MinTokenSize = 32
	MaxTokenSize = 512
)

type Auth struct {
	Token string `json:"token"`
}

type AuthOK struct {
	ConnectionID string    `json:"connection_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type AuthError struct {
	Code string `json:"code"`
}

const (
	AuthInvalid = "AUTH_INVALID"
	AuthExpired = "AUTH_EXPIRED"
	AuthRevoked = "AUTH_REVOKED"
)

func ValidToken(token string) bool {
	if len(token) < MinTokenSize || len(token) > MaxTokenSize {
		return false
	}
	for i := range len(token) {
		if token[i] <= ' ' || token[i] > '~' {
			return false
		}
	}
	return true
}

func EncodeAuth(value Auth) (Frame, error) {
	if !ValidToken(value.Token) {
		return Frame{}, ErrInvalidHandshake
	}
	return encodeHandshake(TypeAuth, value)
}

func DecodeAuth(frame Frame) (Auth, error) {
	var value Auth
	if err := validateHandshakeFrame(frame, TypeAuth); err != nil {
		return Auth{}, err
	}
	if err := decodeObject(frame.Payload, &value, []string{"token"}, []string{"token"}, []string{"token"}); err != nil {
		return Auth{}, err
	}
	if !ValidToken(value.Token) {
		return Auth{}, ErrInvalidHandshake
	}
	return value, nil
}

func validConnectionID(id string) bool {
	if len(id) != 36 || id[:4] != "con_" {
		return false
	}
	for i := 4; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (value AuthOK) Validate() error {
	if !validConnectionID(value.ConnectionID) || value.ExpiresAt.IsZero() {
		return ErrInvalidHandshake
	}
	return nil
}

func EncodeAuthOK(value AuthOK) (Frame, error) {
	if err := value.Validate(); err != nil {
		return Frame{}, err
	}
	return encodeHandshake(TypeAuthOK, value)
}

func DecodeAuthOK(frame Frame) (AuthOK, error) {
	var value AuthOK
	if err := validateHandshakeFrame(frame, TypeAuthOK); err != nil {
		return AuthOK{}, err
	}
	keys := []string{"connection_id", "expires_at"}
	if err := decodeObject(frame.Payload, &value, keys, keys, keys); err != nil {
		return AuthOK{}, err
	}
	if err := value.Validate(); err != nil {
		return AuthOK{}, err
	}
	return value, nil
}

func validAuthCode(code string) bool {
	return code == AuthInvalid || code == AuthExpired || code == AuthRevoked
}

func EncodeAuthError(value AuthError) (Frame, error) {
	if !validAuthCode(value.Code) {
		return Frame{}, ErrInvalidHandshake
	}
	return encodeHandshake(TypeAuthError, value)
}

func DecodeAuthError(frame Frame) (AuthError, error) {
	var value AuthError
	if err := validateHandshakeFrame(frame, TypeAuthError); err != nil {
		return AuthError{}, err
	}
	if err := decodeObject(frame.Payload, &value, []string{"code"}, []string{"code"}, []string{"code"}); err != nil {
		return AuthError{}, err
	}
	if !validAuthCode(value.Code) {
		return AuthError{}, ErrInvalidHandshake
	}
	return value, nil
}
